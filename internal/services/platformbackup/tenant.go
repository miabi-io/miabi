// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
)

// errNoTenantSource means the process asked to RUN this artifact has no tenant
// source, while the process that ENQUEUED it did. That is a wiring fault in this
// build, not something an operator configured wrong — say so, rather than leaving
// them looking for a setting that does not exist.
var errNoTenantSource = errors.New(
	"this process cannot capture tenant data: no tenant source is wired. " +
		"Tenant artifacts are queued by the API server and run by the worker, so every process " +
		"must call EnableTenantCapture — this is a build wiring fault, not a configuration one")

// TenantDatabase is one managed logical database to capture.
type TenantDatabase struct {
	WorkspaceID uint
	Workspace   string // slug, used in the object path
	Instance    *models.DatabaseInstance
	Database    *models.Database
}

// TenantVolume is one workspace-owned Docker volume to capture.
type TenantVolume struct {
	WorkspaceID uint
	Workspace   string
	Name        string // Docker volume name
	ServerID    uint
}

// TenantSource enumerates tenant data and runs a database dump against a chosen
// destination.
//
// It is an interface rather than a direct dependency because dumping a tenant
// database is already solved by services/backup, which knows every engine and
// how to reach a managed instance's network. Platform backup should point that
// machinery at its own bucket, not grow a second copy of it that will drift.
type TenantSource interface {
	ListTenantDatabases() ([]TenantDatabase, error)
	ListTenantVolumes() ([]TenantVolume, error)
	BackupTenantDatabase(ctx context.Context, td TenantDatabase, dest backup.Destination) (*models.Backup, error)
	RestoreTenantDatabase(ctx context.Context, td TenantDatabase, dest backup.Destination, filename string) error
}

// SetTenantSource wires tenant capture. Without one, IncludeTenantData is
// reported as unavailable rather than silently producing control-plane-only
// recovery points that look complete.
func (s *Service) SetTenantSource(src TenantSource) { s.tenants = src }

// TenantCaptureAvailable reports whether tenant data can be included.
func (s *Service) TenantCaptureAvailable() bool { return s.tenants != nil }

// tenantPath is the object prefix for one workspace's artifacts of a kind. The
// layout lives in internal/dr so the restore finds exactly what the backup wrote.
func tenantPath(base, workspace string) string { return dr.TenantPath(base, workspace) }

// slugish reduces a workspace name to something safe in an object key.
func slugish(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// captureTenantData records a pending item per tenant database and volume and
// hands them to the background worker.
//
// They are ENQUEUED, not run here. Dumping every customer database and taring
// every volume takes minutes; doing it inline meant doing it inside the HTTP
// request that asked for the backup, so the moment that request ended — a client
// timeout, a proxy timeout, the handler returning — its context was cancelled and
// every artifact still in flight died with "context canceled". The control-plane
// dump and the platform volumes were already queued; tenant data was the one
// thing that was not, which is why it was the one thing that failed.
func (s *Service) captureTenantData(ctx context.Context, set *models.PlatformBackupSet, st *models.PlatformBackupSettings, cfg *backup.S3Config, trigger string) int {
	if s.tenants == nil {
		logger.Warn("tenant data requested but no tenant source is wired; recovery point will contain the control plane only", "ref", set.Ref)
		return 0
	}
	queued := 0

	dbs, err := s.tenants.ListTenantDatabases()
	if err != nil {
		logger.Error("list tenant databases", "error", err)
	}
	for _, td := range dbs {
		item := &models.PlatformBackup{
			SetID: &set.ID, Subject: models.PlatformBackupTenantDatabase,
			WorkspaceID: td.WorkspaceID, WorkspaceSlug: td.Workspace,
			DatabaseName: td.Database.Name, Engine: string(td.Instance.Engine),
			Status: models.BackupPending, Trigger: trigger, Destination: destS3,
			S3Bucket: cfg.Bucket, S3Path: tenantPath(st.DatabaseBackupPath, td.Workspace),
		}
		if err := s.enqueueTenantItem(ctx, item); err != nil {
			logger.Error("enqueue tenant database backup", "workspace", td.Workspace, "database", td.Database.Name, "error", err)
		}
		queued++
	}

	vols, err := s.tenants.ListTenantVolumes()
	if err != nil {
		logger.Error("list tenant volumes", "error", err)
	}
	for _, tv := range vols {
		item := &models.PlatformBackup{
			SetID: &set.ID, Subject: models.PlatformBackupTenantVolume,
			WorkspaceID: tv.WorkspaceID, WorkspaceSlug: tv.Workspace, VolumeName: tv.Name,
			Status: models.BackupPending, Trigger: trigger, Destination: destS3,
			S3Bucket: cfg.Bucket, S3Path: tenantPath(st.VolumeBackupPath, tv.Workspace),
		}
		if err := s.enqueueTenantItem(ctx, item); err != nil {
			logger.Error("enqueue tenant volume backup", "workspace", tv.Workspace, "volume", tv.Name, "error", err)
		}
		queued++
	}
	return queued
}

// enqueueTenantItem records a tenant artifact and schedules it.
func (s *Service) enqueueTenantItem(ctx context.Context, item *models.PlatformBackup) error {
	if err := s.repo.Create(item); err != nil {
		return err
	}
	if s.enqueuer == nil {
		return s.RunBackup(ctx, item.ID)
	}
	if err := s.enqueuer.EnqueuePlatformBackup(item.ID); err != nil {
		s.fail(item, fmt.Errorf("enqueue backup: %w", err))
		return err
	}
	return nil
}

// resolveTenantDatabase finds the live database an item refers to, by the natural
// key recorded on it. Numeric ids are not used: an item may be retried long after
// it was written, and this keeps the lookup identical to the restore path's.
func (s *Service) resolveTenantDatabase(item *models.PlatformBackup) (TenantDatabase, error) {
	if s.tenants == nil {
		return TenantDatabase{}, errNoTenantSource
	}
	dbs, err := s.tenants.ListTenantDatabases()
	if err != nil {
		return TenantDatabase{}, err
	}
	want := tenantKey(item.WorkspaceSlug, item.DatabaseName)
	for _, td := range dbs {
		if tenantKey(td.Workspace, td.Database.Name) == want {
			return td, nil
		}
	}
	return TenantDatabase{}, fmt.Errorf("database %s/%s no longer exists on this platform", item.WorkspaceSlug, item.DatabaseName)
}

// resolveTenantVolume finds the live volume an item refers to, so the archive
// runs on the node that actually holds it.
func (s *Service) resolveTenantVolume(item *models.PlatformBackup) (TenantVolume, error) {
	if s.tenants == nil {
		return TenantVolume{}, errNoTenantSource
	}
	vols, err := s.tenants.ListTenantVolumes()
	if err != nil {
		return TenantVolume{}, err
	}
	for _, tv := range vols {
		if tv.Name == item.VolumeName {
			return tv, nil
		}
	}
	return TenantVolume{}, fmt.Errorf("volume %q no longer exists on this platform", item.VolumeName)
}

// runTenantDatabaseBackup dumps one tenant database into the platform bucket,
// delegating the engine-specific work to services/backup.
func (s *Service) runTenantDatabaseBackup(ctx context.Context, item *models.PlatformBackup, st *models.PlatformBackupSettings) error {
	cfg, err := s.s3Config(st)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	if cfg == nil {
		s.fail(item, ErrS3NotConfigured)
		return nil
	}
	td, err := s.resolveTenantDatabase(item)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	path := item.S3Path

	now := time.Now()
	item.Status = models.BackupRunning
	item.StartedAt = &now
	_ = s.repo.Update(item)

	dest := backup.Destination{Type: destS3, S3: withPath(cfg, path)}
	// The tenant dump is encrypted with the same passphrase as everything else in
	// the recovery point: one passphrase to custody, one to lose. gpgEnv degrades
	// to unencrypted when there is none, and says so — this must not be the one
	// artifact that fails where the others carried on.
	gpg, _, err := s.gpgEnv(st)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	for _, kv := range gpg {
		if after, ok := strings.CutPrefix(kv, "GPG_PASSPHRASE="); ok {
			dest.GPGPassphrase = after
		}
	}

	rec, err := s.tenants.BackupTenantDatabase(ctx, td, dest)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	if rec == nil || rec.Status != models.BackupCompleted {
		cause := fmt.Errorf("tenant database backup did not complete")
		if rec != nil && rec.Error != "" {
			cause = fmt.Errorf("tenant database backup failed: %s", rec.Error)
		}
		s.fail(item, cause)
		return nil
	}

	fin := time.Now()
	item.Filename = rec.Filename
	item.SizeBytes = rec.SizeBytes
	// What was actually done, not what was asked for: gpgEnv may have degraded.
	item.Encrypted = dest.GPGPassphrase != ""
	item.Status = models.BackupCompleted
	item.FinishedAt = &fin
	if err := s.repo.Update(item); err != nil {
		return err
	}
	s.finalizeSet(item.SetID)
	return nil
}

// runTenantVolumeBackup archives one workspace volume into the platform bucket.
// It uses the same volume-bkup one-shot as platform volumes, on the node that
// actually holds the volume — a workspace volume may live on a worker.
func (s *Service) runTenantVolumeBackup(ctx context.Context, item *models.PlatformBackup, st *models.PlatformBackupSettings) error {
	cfg, err := s.s3Config(st)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	if cfg == nil {
		s.fail(item, ErrS3NotConfigured)
		return nil
	}
	tv, err := s.resolveTenantVolume(item)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	path := item.S3Path

	now := time.Now()
	item.Status = models.BackupRunning
	item.StartedAt = &now
	_ = s.repo.Update(item)

	dc, err := s.clients.For(tv.ServerID)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	gpg, wantEncryption, err := s.gpgEnv(st)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	image := s.volImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		s.fail(item, fmt.Errorf("pull image: %w", err))
		return nil
	}
	// Same as the platform volume: the helper uploads for itself, so it must be
	// able to reach the object store from inside Docker.
	nets, err := s.backupNetworks(ctx, dc)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	out, err := s.runHelper(ctx, dc, "tenant volume backup of "+tv.Name, docker.RunSpec{
		Name:     fmt.Sprintf("mb-tenant-volbkup-%d", item.ID),
		Image:    image,
		Env:      append(backup.S3Env(cfg), gpg...),
		Cmd:      []string{"backup", "--storage", "s3", "--remote-path", path, "--name", volumeArchiveName(tv.Name)},
		Mounts:   map[string]string{tv.Name: volumeMount},
		Networks: nets,
		Labels:   map[string]string{docker.LabelManaged: "true"},
	})
	item.Logs = out
	if err != nil {
		s.fail(item, err)
		return nil
	}

	name, encrypted, err := artifactName(out, volArtifactRe)
	if err != nil {
		s.fail(item, err)
		return nil
	}
	if wantEncryption && !encrypted {
		notEncrypted("tenant volume "+tv.Name, name)
	}
	fin := time.Now()
	item.Filename = name
	item.Encrypted = encrypted
	item.Status = models.BackupCompleted
	item.FinishedAt = &fin
	if err := s.repo.Update(item); err != nil {
		return err
	}
	s.externalizeLog(item)
	s.finalizeSet(item.SetID)
	return nil
}

// withPath copies an S3 config with a specific object prefix.
func withPath(cfg *backup.S3Config, path string) *backup.S3Config {
	out := *cfg
	out.Path = path
	return &out
}
