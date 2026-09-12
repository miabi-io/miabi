// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// Resources are an instance's container limits. Zero means unlimited.
type Resources struct {
	MemoryBytes int64
	NanoCPUs    int64
}

const (
	mebibyte     = 1 << 20
	nanosPerCore = 1_000_000_000
)

// validate refuses negative limits, and memory too small for the engine to start: a limit that low ends with
// the kernel killing the database rather than with a clear error.
func (r Resources) validate(engine models.DBEngine, spec engineSpec) error {
	switch {
	case r.MemoryBytes < 0 || r.NanoCPUs < 0:
		return fmt.Errorf("%w: memory and CPU cannot be negative", ErrInvalidResources)
	case r.MemoryBytes > 0 && r.MemoryBytes < int64(spec.minMemoryMB)*mebibyte:
		return fmt.Errorf("%w: %s needs at least %d MB of memory", ErrInvalidResources, engine, spec.minMemoryMB)
	}
	return nil
}

// withDefaults sizes each dimension the plan caps but the request leaves unset, so an instance created without
// a size, by a marketplace install or a manifest, still counts against the budget.
func (r Resources) withDefaults(spec engineSpec, cpuCapped, memoryCapped bool) Resources {
	if memoryCapped && r.MemoryBytes == 0 {
		r.MemoryBytes = int64(spec.defaultMemoryMB) * mebibyte
	}
	if cpuCapped && r.NanoCPUs == 0 {
		r.NanoCPUs = nanosPerCore
	}
	return r
}

// tuningArgs sizes the engine's own memory use to the container limit. Left alone, an engine sizes itself from
// the host's memory and is killed when it reaches the container's.
func tuningArgs(engine models.DBEngine, memoryBytes int64) []string {
	if memoryBytes <= 0 {
		return nil
	}
	mb := memoryBytes / mebibyte
	switch engine {
	case models.DBEnginePostgres:
		return []string{"-c", fmt.Sprintf("shared_buffers=%dMB", mb/4), "-c", fmt.Sprintf("effective_cache_size=%dMB", mb*3/4)}
	case models.DBEngineMySQL, models.DBEngineMariaDB:
		return []string{fmt.Sprintf("--innodb-buffer-pool-size=%dM", mb*6/10)}
	case models.DBEngineRedis:
		return []string{"--maxmemory", strconv.FormatInt(memoryBytes*8/10, 10)}
	case models.DBEngineMongoDB:
		return []string{"--wiredTigerCacheSizeGB", strconv.FormatFloat(max(0.25, float64(mb-1024)/2/1024), 'f', 2, 64)}
	}
	return nil
}

// Resize sets an instance's CPU and memory limits and recreates its container on the same data volume, so the
// engine is re-tuned to the new memory. The database restarts briefly; a stopped instance stays stopped.
func (s *Service) Resize(ctx context.Context, inst *models.DatabaseInstance, res Resources) (*models.DatabaseInstance, error) {
	if inst.Status == models.DBStatusUpgrading || inst.Status == models.DBStatusProvisioning {
		return nil, ErrInstanceBusy
	}
	spec, ok := specs[inst.Engine]
	if !ok {
		return nil, ErrUnsupportedEngine
	}
	if err := res.validate(inst.Engine, spec); err != nil {
		return nil, err
	}
	cpuCapped, memoryCapped := s.quota.DatabaseComputeCapped(inst.WorkspaceID)
	switch {
	case memoryCapped && res.MemoryBytes == 0:
		return nil, fmt.Errorf("%w: this workspace's plan caps database memory, so the instance needs a memory limit", ErrInvalidResources)
	case cpuCapped && res.NanoCPUs == 0:
		return nil, fmt.Errorf("%w: this workspace's plan caps database CPU, so the instance needs a CPU limit", ErrInvalidResources)
	}
	previous := Resources{MemoryBytes: inst.MemoryBytes, NanoCPUs: inst.NanoCPUs}
	if res == previous {
		return inst, nil
	}
	if err := s.quota.CheckDatabaseComputeAdd(inst.WorkspaceID, res.NanoCPUs, res.MemoryBytes, inst.ID); err != nil {
		return nil, err
	}
	inst.MemoryBytes, inst.NanoCPUs = res.MemoryBytes, res.NanoCPUs
	if err := s.repo.Update(inst); err != nil {
		return nil, err
	}
	// Without a container there is nothing to recreate: the next bring-up starts with the new limits.
	if inst.ContainerID == "" {
		return inst, nil
	}
	if err := s.enqueuer.EnqueueResizeDB(inst.ID, inst.ServerID, previous); err != nil {
		return nil, err
	}
	return inst, nil
}

// RunResize recreates the instance's container with its new limits, on the worker. A container that does not
// come up with them is brought back with the previous limits, so a resize never leaves the database down.
func (s *Service) RunResize(ctx context.Context, instanceID uint, previous Resources) error {
	inst, err := s.repo.FindByID(instanceID)
	if err != nil {
		return err
	}
	spec, ok := specs[inst.Engine]
	if !ok {
		return ErrUnsupportedEngine
	}
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return err
	}
	wasStopped := inst.Status == models.DBStatusStopped
	s.publishProgress(inst, "Applying new resources")
	if err := s.bringUp(ctx, inst, spec, adminPass); err != nil {
		logger.Warn("database resize failed; restoring previous resources", "id", inst.ID, "error", err)
		inst.MemoryBytes, inst.NanoCPUs = previous.MemoryBytes, previous.NanoCPUs
		_ = s.repo.Update(inst)
		msg := "Applying new resources failed, previous resources restored: " + err.Error()
		if rb := s.bringUp(ctx, inst, spec, adminPass); rb != nil {
			inst.Status = models.DBStatusFailed
			_ = s.repo.Update(inst)
			msg = fmt.Sprintf("Applying new resources failed (%v) and restoring the previous ones failed (%v)", err, rb)
		}
		s.publishStatus(inst)
		s.emit(inst, models.EventDatabaseProvisionFailed, models.SeverityError, msg, nil)
		return err
	}
	if wasStopped {
		if dc, derr := s.dockerFor(inst); derr == nil && dc.StopContainer(ctx, inst.ContainerID, 10) == nil {
			inst.Status = models.DBStatusStopped
			_ = s.repo.Update(inst)
		}
	}
	s.publishStatus(inst)
	s.emit(inst, models.EventDatabaseRestarted, models.SeverityInfo, "Restarted with "+describeResources(inst), nil)
	return nil
}

func describeResources(inst *models.DatabaseInstance) string {
	cpu, memory := "unlimited CPU", "unlimited memory"
	if inst.NanoCPUs > 0 {
		cpu = strconv.FormatFloat(float64(inst.NanoCPUs)/nanosPerCore, 'f', -1, 64) + " CPU"
	}
	if inst.MemoryBytes > 0 {
		memory = fmt.Sprintf("%d MB of memory", inst.MemoryBytes/mebibyte)
	}
	return cpu + " and " + memory
}
