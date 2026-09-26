// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	cronpkg "github.com/miabi-io/miabi/internal/cron"
	"github.com/miabi-io/miabi/internal/enterprise"
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
	ee      enterprise.EE
	cron    *cronpkg.Manager
}

func NewVolumeBackupHandler(svc *volumebackup.Service, volumes *repositories.VolumeRepository, repo *repositories.VolumeBackupRepository, cron *cronpkg.Manager, ee enterprise.EE, auditLog *audit.Logger) *VolumeBackupHandler {
	return &VolumeBackupHandler{svc: svc, volumes: volumes, repo: repo, cron: cron, ee: ee, audit: auditLog}
}

// VolumeBackupStatus tells the Backups tab what it can offer. Entitlement rides along because a
// workspace member cannot read the licence view, and a disabled control needs a reason.
type VolumeBackupStatus struct {
	S3Configured bool `json:"s3_configured"`
	// Entitled reports that new backups are recovery points: ref, sealing, verification and
	// schedules. Without it they are plain archives.
	Entitled bool `json:"entitled"`
	// Mutable reports that schedules can be created; false once a licence is past grace.
	Mutable bool `json:"mutable"`
	// Sealing reports that new recovery points will be encrypted under the workspace passphrase.
	Sealing bool `json:"sealing"`
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
	st := VolumeBackupStatus{S3Configured: h.svc.Configured(v.WorkspaceID)}
	if h.ee != nil {
		st.Entitled = h.ee.Require(enterprise.FlagRecoveryPoints) == nil
		st.Mutable = h.ee.RequireMutable(enterprise.FlagRecoveryPoints) == nil
	}
	st.Sealing = st.Entitled && h.svc.Sealing(v.WorkspaceID)
	return ok(c, st)
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
// returned record starts pending; its status advances as the worker runs. With the
// recovery_points entitlement it is a recovery point; without, a plain archive.
func (h *VolumeBackupHandler) Run(c *okapi.Context) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	var b *models.VolumeBackup
	if h.entitled() {
		b, err = h.svc.CreatePoint(c.Request().Context(), v, "manual", nil)
	} else {
		b, err = h.svc.Create(c.Request().Context(), v, "manual")
	}
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
		case errors.Is(err, volumebackup.ErrPassphraseRequired):
			return c.AbortBadRequest(err.Error())
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

// Verify re-checks a completed backup against the bucket. Ungated: an expired licence must
// never stop anyone from finding out whether their data is still there.
func (h *VolumeBackupHandler) Verify(c *okapi.Context) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	b, err := h.loadBackup(c, v.WorkspaceID)
	if err != nil || b.VolumeID != v.ID {
		return c.AbortNotFound("volume backup not found")
	}
	res, err := h.svc.Verify(c.Request().Context(), b)
	switch {
	case errors.Is(err, volumebackup.ErrNotCompleted):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, volumebackup.ErrS3NotConfigured):
		return c.AbortBadRequest("configure S3 backup settings for this workspace first")
	case err != nil:
		return c.AbortInternalServerError("failed to verify the backup", err)
	}
	h.record(c, v.WorkspaceID, "volume.backup_verify", b.ID)
	return ok(c, res)
}

// UpdateVolumeBackupRequest changes a recovery point's retention exemption.
type UpdateVolumeBackupRequest struct {
	Body struct {
		Pinned bool `json:"pinned"`
	} `json:"body"`
}

// Update pins or unpins a recovery point.
func (h *VolumeBackupHandler) Update(c *okapi.Context, req *UpdateVolumeBackupRequest) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	b, err := h.loadBackup(c, v.WorkspaceID)
	if err != nil || b.VolumeID != v.ID {
		return c.AbortNotFound("volume backup not found")
	}
	if err := h.svc.SetPinned(b, req.Body.Pinned); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	action := "volume.backup_unpin"
	if req.Body.Pinned {
		action = "volume.backup_pin"
	}
	h.record(c, v.WorkspaceID, action, b.ID)
	return ok(c, b)
}

// VolumeBackupScheduleRequest creates or replaces a volume's recovery-point schedule.
type VolumeBackupScheduleRequest struct {
	Body struct {
		Cron string `json:"cron" required:"true"`
		// MaxPoints keeps at most N points; RetentionDays drops any older than N days. Zero is
		// unbounded. Pinned points and the newest completed one always survive.
		MaxPoints     int  `json:"max_points" min:"0"`
		RetentionDays int  `json:"retention_days" min:"0"`
		Enabled       bool `json:"enabled"`
	} `json:"body"`
}

// ListSchedules returns a volume's recovery-point schedules.
func (h *VolumeBackupHandler) ListSchedules(c *okapi.Context) error {
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	out, err := h.svc.ListSchedules(v.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list schedules", err)
	}
	return ok(c, out)
}

// CreateSchedule schedules recovery points for the volume (Enterprise; recovery_points).
func (h *VolumeBackupHandler) CreateSchedule(c *okapi.Context, req *VolumeBackupScheduleRequest) error {
	if err := h.requireMutable(); err != nil {
		return entitlementAbort(c, err)
	}
	v, err := h.loadVolume(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	sched := &models.VolumeBackupSchedule{WorkspaceID: v.WorkspaceID, VolumeID: v.ID}
	if msg := h.applySchedule(v, sched, req); msg != "" {
		return c.AbortBadRequest(msg)
	}
	if err := h.repo.CreateSchedule(sched); err != nil {
		return c.AbortInternalServerError("failed to create the schedule", err)
	}
	h.syncCron(sched)
	h.record(c, v.WorkspaceID, "volume.backup_schedule_create", sched.ID)
	return ok(c, sched)
}

// UpdateSchedule edits a volume's recovery-point schedule (Enterprise; recovery_points). Pausing
// is exempt from the gate, like deleting: a lapsed licence must still be able to stop a schedule.
func (h *VolumeBackupHandler) UpdateSchedule(c *okapi.Context, req *VolumeBackupScheduleRequest) error {
	v, sched, err := h.loadSchedule(c)
	if err != nil {
		return c.AbortNotFound("schedule not found")
	}
	if !pausesOnly(sched, req) {
		if err := h.requireMutable(); err != nil {
			return entitlementAbort(c, err)
		}
	}
	if msg := h.applySchedule(v, sched, req); msg != "" {
		return c.AbortBadRequest(msg)
	}
	if err := h.repo.UpdateSchedule(sched); err != nil {
		return c.AbortInternalServerError("failed to update the schedule", err)
	}
	h.syncCron(sched)
	h.record(c, v.WorkspaceID, "volume.backup_schedule_update", sched.ID)
	return ok(c, sched)
}

// DeleteSchedule removes a recovery-point schedule. Ungated, so an expired licence can still
// stop one.
func (h *VolumeBackupHandler) DeleteSchedule(c *okapi.Context) error {
	v, sched, err := h.loadSchedule(c)
	if err != nil {
		return c.AbortNotFound("schedule not found")
	}
	if err := h.repo.DeleteSchedule(sched.ID); err != nil {
		return c.AbortInternalServerError("failed to delete the schedule", err)
	}
	if h.cron != nil {
		h.cron.UnregisterVolumeBackup(sched.ID)
	}
	h.record(c, v.WorkspaceID, "volume.backup_schedule_delete", sched.ID)
	return ok(c, okapi.M{"deleted": true})
}

// pausesOnly reports whether req changes nothing but switching sched off.
func pausesOnly(sched *models.VolumeBackupSchedule, req *VolumeBackupScheduleRequest) bool {
	return !req.Body.Enabled &&
		strings.TrimSpace(req.Body.Cron) == sched.Cron &&
		req.Body.MaxPoints == sched.MaxPoints &&
		req.Body.RetentionDays == sched.RetentionDays
}

// applySchedule validates a request onto sched, returning a user-facing reason on refusal.
func (h *VolumeBackupHandler) applySchedule(v *models.Volume, sched *models.VolumeBackupSchedule, req *VolumeBackupScheduleRequest) string {
	spec := strings.TrimSpace(req.Body.Cron)
	if err := cronpkg.ValidateSpec(spec); err != nil {
		return "invalid cron expression: " + err.Error()
	}
	if req.Body.MaxPoints < 0 || req.Body.RetentionDays < 0 {
		return "retention values cannot be negative"
	}
	// Refuse a schedule that could only ever fail, rather than letting it discover that at 03:00.
	if req.Body.Enabled && !h.svc.Configured(v.WorkspaceID) {
		return "recovery points need the workspace S3 backup target — configure it under Workspace settings → Backups"
	}
	sched.Cron = spec
	sched.Enabled = req.Body.Enabled
	sched.MaxPoints = req.Body.MaxPoints
	sched.RetentionDays = req.Body.RetentionDays
	return ""
}

func (h *VolumeBackupHandler) syncCron(sched *models.VolumeBackupSchedule) {
	if h.cron == nil {
		return
	}
	if sched.Enabled {
		h.cron.RegisterVolumeBackup(*sched)
		return
	}
	h.cron.UnregisterVolumeBackup(sched.ID)
}

func (h *VolumeBackupHandler) loadSchedule(c *okapi.Context) (*models.Volume, *models.VolumeBackupSchedule, error) {
	v, err := h.loadVolume(c)
	if err != nil {
		return nil, nil, err
	}
	id, err := strconv.Atoi(c.Param("scheduleID"))
	if err != nil || id <= 0 {
		return nil, nil, errors.New("invalid schedule id")
	}
	sched, err := h.repo.FindScheduleInWorkspace(v.WorkspaceID, uint(id))
	if err != nil {
		return nil, nil, err
	}
	if sched.VolumeID != v.ID {
		return nil, nil, errors.New("schedule belongs to another volume")
	}
	return v, sched, nil
}

func (h *VolumeBackupHandler) entitled() bool {
	return h.ee != nil && h.ee.Require(enterprise.FlagRecoveryPoints) == nil
}

func (h *VolumeBackupHandler) requireMutable() error {
	if h.ee == nil {
		return enterprise.ErrLicenseRequired
	}
	return h.ee.RequireMutable(enterprise.FlagRecoveryPoints)
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
