// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/platformimage"
)

// sizeSyncTTL is how long a synced size is considered fresh; a lazy refresh
// triggers only past this age.
const sizeSyncTTL = 24 * time.Hour

// MaybeRefreshSizes lazily refreshes an instance's sizes in the background when they are stale and the
// instance is running. It never blocks the caller and debounces concurrent triggers per instance, so a
// detail-page load returns immediately with possibly-stale values and the next one shows fresh ones.
func (s *Service) MaybeRefreshSizes(inst *models.DatabaseInstance) {
	if inst == nil || inst.Status != models.DBStatusRunning {
		return
	}
	if inst.SizeSyncedAt != nil && time.Since(*inst.SizeSyncedAt) < sizeSyncTTL {
		return
	}
	if _, busy := s.sizeSyncing.LoadOrStore(inst.ID, true); busy {
		return // a refresh is already running for this instance
	}
	id := inst.ID
	go func() {
		defer s.sizeSyncing.Delete(id)
		fresh, err := s.repo.FindByID(id)
		if err != nil {
			return
		}
		if err := s.SyncSizes(context.Background(), fresh); err != nil {
			logger.Warn("background size refresh failed", "instance", id, "error", err)
		}
	}()
}

// syncAllTimeout bounds one instance's probe during the nightly sweep. Each engine is queried
// through a container exec, so a wedged instance would otherwise hold the whole sweep behind it.
const syncAllTimeout = 2 * time.Minute

// SyncAllSizes refreshes every running instance's sizes. Without it the numbers are only ever
// refreshed lazily, when someone opens a detail page and the value is a day old — so a database
// nobody looks at is never measured, and the usage totals that aggregate it drift indefinitely.
func (s *Service) SyncAllSizes(ctx context.Context) (int, error) {
	list, err := s.repo.ListAllInstances()
	if err != nil {
		return 0, fmt.Errorf("list database instances: %w", err)
	}
	measured, skipped := 0, 0
	for i := range list {
		if ctx.Err() != nil {
			logger.Warn("database size sweep cancelled", "measured", measured, "remaining", len(list)-i)
			return measured, ctx.Err()
		}
		inst := &list[i]
		// Only a running instance can answer. A stopped one keeps its last known size rather than
		// being zeroed, which would read as "this database lost its data".
		if inst.Status != models.DBStatusRunning {
			skipped++
			continue
		}
		one, cancel := context.WithTimeout(ctx, syncAllTimeout)
		err := s.SyncSizes(one, inst)
		cancel()
		if err != nil {
			logger.Warn("database size sweep: instance failed",
				"instance", inst.ID, "name", inst.Name, "engine", inst.Engine, "error", err)
			continue
		}
		measured++
	}
	logger.Info("database size sweep complete", "measured", measured, "skipped", skipped, "total", len(list))
	return measured, nil
}

// SyncSizes refreshes the on-disk size of an instance and its logical databases
// by querying the engine. Requires the instance to be running.
func (s *Service) SyncSizes(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status != models.DBStatusRunning {
		return ErrInstanceNotReady
	}
	now := time.Now()

	if inst.Engine == models.DBEngineRedis {
		out, err := s.execQuery(ctx, inst, "INFO memory")
		if err != nil {
			return err
		}
		inst.SizeBytes = parseRedisUsedMemory(out)
		inst.SizeSyncedAt = &now
		return s.repo.Update(inst)
	}

	if inst.Engine == models.DBEngineLibSQL {
		size, err := s.libsqlDiskSize(ctx, inst)
		if err != nil {
			return err
		}
		inst.SizeBytes = size
		inst.SizeSyncedAt = &now
		if dbs, derr := s.repo.ListDatabases(inst.ID); derr == nil {
			for i := range dbs {
				dbs[i].SizeBytes = size
				dbs[i].SizeSyncedAt = &now
				_ = s.repo.UpdateDatabase(&dbs[i])
			}
		}
		return s.repo.Update(inst)
	}

	var query string
	switch inst.Engine {
	case models.DBEnginePostgres:
		query = "SELECT datname, pg_database_size(datname) FROM pg_database WHERE NOT datistemplate"
	case models.DBEngineMongoDB:
		// Emit "<db>\t<sizeOnDisk>" per database so parseSizeRows handles it like
		// the SQL engines' tab-separated output.
		query = `db.adminCommand({listDatabases:1}).databases.forEach(function(d){print(d.name+"\t"+d.sizeOnDisk)})`
	default: // mysql / mariadb
		query = "SELECT table_schema, COALESCE(SUM(data_length+index_length),0) FROM information_schema.tables GROUP BY table_schema"
	}
	out, err := s.execQuery(ctx, inst, query)
	if err != nil {
		return err
	}
	sizes := parseSizeRows(out)

	dbs, err := s.repo.ListDatabases(inst.ID)
	if err != nil {
		return err
	}
	var total int64
	for i := range dbs {
		if sz, ok := sizes[dbs[i].Name]; ok {
			dbs[i].SizeBytes = sz
			dbs[i].SizeSyncedAt = &now
			_ = s.repo.UpdateDatabase(&dbs[i])
		}
		total += dbs[i].SizeBytes
	}
	inst.SizeBytes = total
	inst.SizeSyncedAt = &now
	return s.repo.Update(inst)
}

// libsqlDiskSize measures the on-disk size of a libSQL instance's data directory
// by running `du -sb` in a tiny helper container with the instance's data volume
// mounted (libSQL exposes no SQL/CLI size probe).
func (s *Service) libsqlDiskSize(ctx context.Context, inst *models.DatabaseInstance) (int64, error) {
	dc, err := s.dockerFor(inst)
	if err != nil {
		return 0, err
	}
	image := s.helperImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return 0, fmt.Errorf("pull helper image: %w", err)
	}
	const mnt = "/data"
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:       fmt.Sprintf("mb-dbsize-%d-%d", inst.ID, time.Now().UnixNano()%100000),
		Image:      image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"du -sb " + mnt + " | cut -f1"},
		Mounts:     map[string]string{inst.VolumeName: mnt},
	})
	if err != nil || exit != 0 {
		return 0, fmt.Errorf("size probe exited %d: %s", exit, strings.TrimSpace(out))
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size %q: %w", strings.TrimSpace(out), err)
	}
	return n, nil
}

// helperImage is the tiny image used for volume operations (resolved from the
// deployment-config catalog, else busybox).
func (s *Service) helperImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyHelper); r != "" {
			return r
		}
	}
	return "busybox:1.36"
}

func (s *Service) execQuery(ctx context.Context, inst *models.DatabaseInstance, query string) (string, error) {
	spec := specs[inst.Engine]
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return "", err
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return "", err
	}
	nets, err := s.ensureInstanceNetworks(ctx, dc, inst)
	if err != nil {
		return "", err
	}
	image := s.engineImage(spec, inst)
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return "", fmt.Errorf("pull client image: %w", err)
	}
	cmd, env := queryInvocation(inst, query, adminPass)
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:     fmt.Sprintf("mb-dbq-%d-%d", inst.ID, time.Now().UnixNano()%100000),
		Image:    image,
		Env:      env,
		Cmd:      cmd,
		Networks: nets,
	})
	if err != nil || exit != 0 {
		return out, fmt.Errorf("size query exited %d: %s", exit, strings.TrimSpace(out))
	}
	return out, nil
}

// parseSizeRows parses "name|size" (postgres) or "name<TAB>size" (mysql) lines
// into a name->bytes map.
func parseSizeRows(out string) map[string]int64 {
	sizes := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.FieldsFunc(line, func(r rune) bool { return r == '|' || r == '\t' })
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		if n, err := strconv.ParseInt(strings.TrimSpace(fields[len(fields)-1]), 10, 64); err == nil {
			sizes[name] = n
		}
	}
	return sizes
}

func parseRedisUsedMemory(out string) int64 {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "used_memory:"); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}
