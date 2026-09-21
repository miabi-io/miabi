// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/volumebackup"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// VolumeBackupHandler exposes backup/restore of a managed volume's contents.
type VolumeBackupHandler struct {
	svc     *volumebackup.Service
	volumes *repositories.VolumeRepository
	repo    *repositories.VolumeBackupRepository
	audit   *audit.Logger
	logs    *logstore.Store
}

func NewVolumeBackupHandler(svc *volumebackup.Service, volumes *repositories.VolumeRepository, repo *repositories.VolumeBackupRepository, auditLog *audit.Logger) *VolumeBackupHandler {
	return &VolumeBackupHandler{svc: svc, volumes: volumes, repo: repo, audit: auditLog}
}

// SetLogStore wires the shared execution-log store so a volume-backup run's full
// log can be downloaded from the store (falling back to the DB tail). nil keeps
// tail-only.
func (h *VolumeBackupHandler) SetLogStore(s *logstore.Store) { h.logs = s }

// LogsDownload streams a volume-backup run's full log as a file download.
func (h *VolumeBackupHandler) LogsDownload(c *okapi.Context) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	b, err := h.loadBackup(c, v.WorkspaceID)
	if err != nil {
		return c.AbortNotFound("backup not found")
	}
	return streamLogDownload(c, h.logs, b.LogRef, b.Logs, "volume-backup-"+strconv.FormatUint(uint64(b.ID), 10)+".log")
}

// Status reports whether volume backups are configured for the workspace (S3
// enabled + bucket), so the UI can block the action before the user triggers it.
func (h *VolumeBackupHandler) Status(c *okapi.Context) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	return ok(c, map[string]bool{"s3_configured": h.svc.Configured(v.WorkspaceID)})
}

// List returns a volume's backup history.
func (h *VolumeBackupHandler) List(c *okapi.Context) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	items, err := h.svc.List(v.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list volume backups", err)
	}
	return ok(c, items)
}

// Run records a manual volume backup and enqueues it for the worker. The
// returned record starts pending; its status advances as the worker runs.
func (h *VolumeBackupHandler) Run(c *okapi.Context) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	b, err := h.svc.Create(c.Request().Context(), v, "manual")
	if err != nil {
		if errors.Is(err, volumebackup.ErrS3NotConfigured) {
			return c.AbortBadRequest("configure S3 backup settings for this workspace first")
		}
		return c.AbortInternalServerError("failed to back up volume", err)
	}
	h.record(c, v.WorkspaceID, "volume.backup", b.ID)
	return ok(c, b)
}

// Restore restores a volume from one of its backups (overwrites volume data).
func (h *VolumeBackupHandler) Restore(c *okapi.Context) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	b, err := h.loadBackup(c, v.WorkspaceID)
	if err != nil {
		return c.AbortNotFound("volume backup not found")
	}
	if err := h.svc.Restore(c.Request().Context(), v, b); err != nil {
		switch {
		case errors.Is(err, volumebackup.ErrS3NotConfigured):
			return c.AbortBadRequest("configure S3 backup settings for this workspace first")
		case errors.Is(err, volumebackup.ErrNoArchive):
			return c.AbortBadRequest("this backup has no archive to restore")
		default:
			return c.AbortInternalServerError("restore failed", err)
		}
	}
	h.record(c, v.WorkspaceID, "volume.restore", b.ID)
	return message(c, "volume restored")
}

// Delete removes a volume backup record. In-flight backups can't be deleted.
func (h *VolumeBackupHandler) Delete(c *okapi.Context) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	b, err := h.loadBackup(c, v.WorkspaceID)
	if err != nil {
		return c.AbortNotFound("volume backup not found")
	}
	if b.Status == models.BackupPending || b.Status == models.BackupRunning {
		return c.AbortBadRequest("cannot delete a backup that is still running")
	}
	if err := h.svc.Delete(c.Request().Context(), b); err != nil {
		return c.AbortInternalServerError("failed to delete backup", err)
	}
	h.record(c, v.WorkspaceID, "volume.backup_delete", b.ID)
	return message(c, "backup deleted")
}

func (h *VolumeBackupHandler) loadVolume(c *okapi.Context) (*models.Volume, error) {
	id, err := strconv.Atoi(c.Param("volumeID"))
	if err != nil || id <= 0 {
		return nil, errors.New("invalid volume id")
	}
	return h.volumes.FindInWorkspace(middlewares.WorkspaceID(c), uint(id))
}

func (h *VolumeBackupHandler) loadBackup(c *okapi.Context, workspaceID uint) (*models.VolumeBackup, error) {
	id, err := strconv.Atoi(c.Param("backupID"))
	if err != nil || id <= 0 {
		return nil, errors.New("invalid backup id")
	}
	return h.repo.FindInWorkspace(workspaceID, uint(id))
}

func (h *VolumeBackupHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "volume_backup", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
