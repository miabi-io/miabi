// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package volumebackup archives a managed volume's contents to the workspace's S3 target using the ecosystem
// volume-bkup tool as a one-shot container, and restores it. Runs synchronously, mirroring the database
// backup service. S3 is required — there is no local volume-backup destination.
package volumebackup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	// ErrS3NotConfigured is returned when the workspace has no S3 backup target.
	ErrS3NotConfigured = errors.New("workspace S3 backup settings are not configured")
	// ErrNoArchive is returned when restoring a backup that has no archive.
	ErrNoArchive = errors.New("volume backup has no archive to restore")

	// volume-bkup names archives like <name>_YYYYMMDD_HHMMSS.tar.gz.
	archiveRe = regexp.MustCompile(`[\w.\-]+\.tar\.gz`)
)

const (
	volumeMount      = "/data"
	defaultBkupImage = "jkaninda/volume-bkup:latest"
)

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// S3Provider yields a workspace's S3 target plus the volume backup path prefix.
// Satisfied by backupsettings.Service.
type S3Provider interface {
	VolumeBackupTarget(workspaceID uint) (*backup.S3Config, string, error)
}

// Enqueuer schedules a volume backup to run in the background worker. Satisfied
// by worker.Producer.
type Enqueuer interface {
	EnqueueVolumeBackup(backupID, serverID uint) error
}

type Service struct {
	repo     *repositories.VolumeBackupRepository
	volumes  *repositories.VolumeRepository
	clients  NodeDocker
	images   ImageResolver
	s3       S3Provider
	enqueuer Enqueuer
	logs     *logstore.Store
}

func NewService(repo *repositories.VolumeBackupRepository, volumes *repositories.VolumeRepository, clients NodeDocker) *Service {
	return &Service{repo: repo, volumes: volumes, clients: clients}
}

// SetImageResolver wires the deployment-config resolver for the volume-bkup image.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// SetLogStore wires the shared execution-log store. When set, a volume-backup
// run's full output is externalized to the store on terminal state and the DB
// row keeps only a bounded tail + a reference. nil keeps DB-tail-only.
func (s *Service) SetLogStore(store *logstore.Store) { s.logs = store }

// externalizeLog moves a terminal volume-backup's full output into the shared log store and trims the row
// to a bounded tail + a reference. No-op when the store is disabled or already externalized; on any error
// the full log stays in the DB tail.
func (s *Service) externalizeLog(b *models.VolumeBackup) {
	if !s.logs.Enabled() || b.LogRef != "" {
		return
	}
	ref := logstore.VolumeBackupRef(b.WorkspaceID, b.ID)
	res, err := s.logs.Externalize(ref, b.Logs)
	if err != nil {
		logger.Error("log store: externalize volume backup log failed", "volume_backup", b.ID, "error", err)
		return
	}
	if err := s.repo.SetLogMeta(b.ID, res.Ref, res.Tail, res.Bytes, res.Lines, res.Truncated); err != nil {
		logger.Error("log store: record volume backup log ref failed", "volume_backup", b.ID, "error", err)
		return
	}
	b.LogRef, b.Logs = res.Ref, res.Tail
	b.LogBytes, b.LogLines, b.LogTruncated = res.Bytes, res.Lines, res.Truncated
}

// SetS3Provider wires the workspace S3 settings provider.
func (s *Service) SetS3Provider(p S3Provider) { s.s3 = p }

// SetEnqueuer wires the background worker producer. When unset, Create runs the
// backup synchronously (used in tests / no-redis setups).
func (s *Service) SetEnqueuer(e Enqueuer) { s.enqueuer = e }

func (s *Service) image() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyBackupVolume); r != "" {
			return r
		}
	}
	return defaultBkupImage
}

// List returns a volume's backup history (most recent first).
func (s *Service) List(volumeID uint) ([]models.VolumeBackup, error) {
	return s.repo.ListByVolume(volumeID)
}

// Configured reports whether the workspace has a usable S3 backup target (S3
// enabled with a bucket). Volume backups are blocked when this is false; the UI
// uses it to disable the action before the user triggers a 400.
func (s *Service) Configured(workspaceID uint) bool {
	cfg, _, err := s.target(workspaceID)
	return err == nil && cfg != nil
}

// Delete removes a volume backup record. The S3 archive object is left in place
// (mirrors database backup deletion — Miabi has no S3 client and volume-bkup
// has no delete command); reclaim S3 storage with a bucket lifecycle rule.
func (s *Service) Delete(b *models.VolumeBackup) error {
	return s.repo.Delete(b.ID)
}

// Create records a pending volume backup and enqueues it for the background
// worker, returning the pending record immediately. With no enqueuer wired it
// runs the backup synchronously. Validates that S3 is configured.
func (s *Service) Create(ctx context.Context, vol *models.Volume, trigger string) (*models.VolumeBackup, error) {
	cfg, path, err := s.target(vol.WorkspaceID)
	if err != nil {
		return nil, err
	}
	b := &models.VolumeBackup{
		WorkspaceID: vol.WorkspaceID, VolumeID: vol.ID, ServerID: vol.ServerID,
		VolumeName: vol.DockerName, Status: models.BackupPending, Trigger: trigger,
		S3Bucket: cfg.Bucket, S3Path: path,
	}
	if err := s.repo.Create(b); err != nil {
		return nil, err
	}
	if s.enqueuer == nil {
		// No worker wired — run inline.
		_ = s.RunBackup(ctx, b.ID)
		return s.repo.FindByID(b.ID)
	}
	if err := s.enqueuer.EnqueueVolumeBackup(b.ID, vol.ServerID); err != nil {
		return s.fail(b, fmt.Errorf("enqueue backup: %w", err)), nil
	}
	return b, nil
}

// RunBackup executes a pending volume backup (the worker entry point): it loads
// the record, archives the volume to S3, and records the outcome. Handled
// failures are recorded on the row and return nil (no auto-retry).
func (s *Service) RunBackup(ctx context.Context, backupID uint) error {
	b, err := s.repo.FindByID(backupID)
	if err != nil {
		return fmt.Errorf("volume backup %d not found: %w", backupID, err)
	}
	if b.Status == models.BackupCompleted || b.Status == models.BackupFailed {
		return nil // already processed
	}
	vol, err := s.volumes.FindInWorkspace(b.WorkspaceID, b.VolumeID)
	if err != nil {
		s.fail(b, fmt.Errorf("volume not found: %w", err))
		return nil
	}
	cfg, _, err := s.target(vol.WorkspaceID)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	dc, err := s.clients.For(vol.ServerID)
	if err != nil {
		s.fail(b, err)
		return nil
	}

	now := time.Now()
	b.Status = models.BackupRunning
	b.StartedAt = &now
	_ = s.repo.Update(b)

	image := s.image()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		s.fail(b, fmt.Errorf("pull image: %w", err))
		return nil
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:   fmt.Sprintf("mb-volbkup-%d", b.ID),
		Image:  image,
		Env:    backup.S3Env(cfg),
		Cmd:    []string{"backup", "--storage", "s3", "--remote-path", b.S3Path, "--name", vol.Name},
		Mounts: map[string]string{vol.DockerName: volumeMount},
		Labels: map[string]string{
			docker.LabelWorkspace: fmt.Sprintf("%d", vol.WorkspaceID),
			docker.LabelVolume:    fmt.Sprintf("%d", vol.ID),
		},
	})
	b.Logs = out
	if err != nil {
		s.fail(b, err)
		return nil
	}
	if exit != 0 {
		s.fail(b, fmt.Errorf("volume backup exited %d", exit))
		return nil
	}

	b.Filename = archiveRe.FindString(out)
	fin := time.Now()
	b.Status = models.BackupCompleted
	b.FinishedAt = &fin
	if err := s.repo.Update(b); err != nil {
		return err
	}
	s.externalizeLog(b)
	logger.Info("volume backup completed", "volume", vol.ID, "file", b.Filename)
	return nil
}

// Restore extracts a previously-created archive back into the volume, mounting
// it read-write at /data. This overwrites existing data.
func (s *Service) Restore(ctx context.Context, vol *models.Volume, b *models.VolumeBackup) error {
	if b.Filename == "" {
		return ErrNoArchive
	}
	cfg, _, err := s.target(vol.WorkspaceID)
	if err != nil {
		return err
	}
	dc, err := s.clients.For(vol.ServerID)
	if err != nil {
		return err
	}
	image := s.image()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:   fmt.Sprintf("mb-volrestore-%d", b.ID),
		Image:  image,
		Env:    backup.S3Env(cfg),
		Cmd:    []string{"restore", "--storage", "s3", "--remote-path", b.S3Path, "--file", b.Filename},
		Mounts: map[string]string{vol.DockerName: volumeMount},
	})
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("volume restore exited %d: %s", exit, out)
	}
	return nil
}

// target resolves the workspace's S3 config + volume path, returning
// ErrS3NotConfigured when S3 is not set up.
func (s *Service) target(workspaceID uint) (*backup.S3Config, string, error) {
	if s.s3 == nil {
		return nil, "", ErrS3NotConfigured
	}
	cfg, path, err := s.s3.VolumeBackupTarget(workspaceID)
	if err != nil {
		return nil, "", err
	}
	if cfg == nil {
		return nil, "", ErrS3NotConfigured
	}
	return cfg, path, nil
}

// fail marks a backup record failed and returns it.
func (s *Service) fail(b *models.VolumeBackup, cause error) *models.VolumeBackup {
	fin := time.Now()
	b.Status = models.BackupFailed
	b.Error = cause.Error()
	b.FinishedAt = &fin
	_ = s.repo.Update(b)
	s.externalizeLog(b)
	logger.Error("volume backup failed", "volume", b.VolumeID, "error", cause)
	return b
}
