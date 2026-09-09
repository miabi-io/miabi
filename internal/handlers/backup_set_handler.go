// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
)

// RunBackupSetRequest triggers a recovery point across every database on an
// instance.
type RunBackupSetRequest struct {
	Body struct {
		// Comment is attached to every backup in the set, so the reason a set was
		// taken is visible on its items too.
		Comment string `json:"comment"`
		// Concurrency caps how many databases are dumped at once. Zero uses the
		// service default, which is deliberately conservative: parallel dumps
		// against one engine are felt by whatever else is using it.
		Concurrency int `json:"concurrency"`
	} `json:"body"`
}

// BackupSetsResponse carries the recovery points plus whether the workspace can
// take one at all. The capability rides along because a developer can open this
// page without being able to read the workspace's backup settings, and a disabled
// button with no reason is worse than no button.
type BackupSetsResponse struct {
	Sets []models.DatabaseBackupSet `json:"sets"`
	// S3Configured reports that the workspace has an object-storage target. Sets
	// require one: see backup.ErrS3Required.
	S3Configured bool `json:"s3_configured"`
}

// ListSets returns an instance's recovery points, newest first.
func (h *BackupHandler) ListSets(c *okapi.Context) error {
	inst, err := h.loadInstance(c)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	sets, err := h.svc.ListSets(inst.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list backup sets", err)
	}
	return ok(c, BackupSetsResponse{Sets: sets, S3Configured: h.s3Configured(inst.WorkspaceID)})
}

// s3Configured reports whether the workspace has a usable object-storage target.
func (h *BackupHandler) s3Configured(workspaceID uint) bool {
	if h.settings == nil {
		return false
	}
	cfg, _, err := h.settings.DatabaseBackupTarget(workspaceID)
	return err == nil && cfg != nil
}

// RunSet backs up every database on the instance as one recovery point.
func (h *BackupHandler) RunSet(c *okapi.Context, req *RunBackupSetRequest) error {
	inst, err := h.loadInstance(c)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	dest, err := h.setDestination(inst.WorkspaceID)
	if err != nil {
		return c.AbortInternalServerError("failed to resolve the backup destination", err)
	}
	set, err := h.svc.RunSet(c.Request().Context(), inst, backup.SetOptions{
		Trigger:     "manual",
		Comment:     req.Body.Comment,
		Concurrency: req.Body.Concurrency,
	}, dest)
	switch {
	case errors.Is(err, backup.ErrS3Required):
		return c.AbortBadRequest("recovery points need the workspace S3 backup target — configure it under Workspace settings → Backups")
	case errors.Is(err, backup.ErrNoDatabases):
		return c.AbortBadRequest("this instance has no database to back up")
	case errors.Is(err, backup.ErrUnsupportedEngine):
		return c.AbortBadRequest("this engine does not support database backups")
	case err != nil:
		return c.AbortInternalServerError("failed to run the backup set", err)
	}
	h.record(c, inst.WorkspaceID, "database.backup_set_run", set.ID)
	return ok(c, set)
}

// DeleteSet removes a recovery point and every artifact it carries.
func (h *BackupHandler) DeleteSet(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	set, err := h.loadSet(c, wsID)
	if err != nil {
		return c.AbortNotFound("backup set not found")
	}
	if err := h.svc.DeleteSet(c.Request().Context(), set); err != nil {
		return c.AbortInternalServerError("failed to delete the backup set", err)
	}
	h.record(c, wsID, "database.backup_set_delete", set.ID)
	return ok(c, okapi.M{"deleted": true})
}

// setDestination resolves where a set is written. It is the same workspace target
// manual and scheduled per-database backups use, so a set is encrypted on exactly
// the same terms as they are.
func (h *BackupHandler) setDestination(workspaceID uint) (backup.Destination, error) {
	if h.settings == nil {
		return backup.Destination{Type: "local"}, nil
	}
	return h.settings.DatabaseDestination(workspaceID)
}

func (h *BackupHandler) loadInstance(c *okapi.Context) (*models.DatabaseInstance, error) {
	id, err := strconv.Atoi(c.Param("databaseID"))
	if err != nil || id <= 0 {
		return nil, errors.New("invalid instance id")
	}
	return h.dbs.FindInWorkspace(middlewares.WorkspaceID(c), uint(id))
}

func (h *BackupHandler) loadSet(c *okapi.Context, workspaceID uint) (*models.DatabaseBackupSet, error) {
	id, err := strconv.Atoi(c.Param("setID"))
	if err != nil || id <= 0 {
		return nil, errors.New("invalid set id")
	}
	return h.sets.FindInWorkspace(workspaceID, uint(id))
}

// DiscoverSets lists the recovery points in the workspace's bucket, including any
// this platform has no record of. Read-only: it reads the cleartext descriptors and
// writes nothing.
func (h *BackupHandler) DiscoverSets(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if h.settings == nil {
		return c.AbortBadRequest("workspace backup settings are not available")
	}
	cfg, base, err := h.settings.DatabaseBackupTarget(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to read the backup target", err)
	}
	if cfg == nil {
		return c.AbortBadRequest("recovery points need the workspace S3 backup target — configure it under Workspace settings → Backups")
	}
	// Optional: without it the list still shows what exists, just not what opens.
	pass, _ := h.settings.DatabaseBackupPassphrase(wsID)

	found, err := h.svc.DiscoverSets(c.Request().Context(), cfg, base, pass)
	if err != nil {
		return c.AbortInternalServerError("failed to read the bucket", err)
	}
	return ok(c, found)
}

// AdoptSetRequest names the recovery point to pull into this workspace's history.
type AdoptSetRequest struct {
	Body struct {
		Ref string `json:"ref" required:"true"`
	} `json:"body"`
}

// AdoptSet writes a recovery point found in the bucket into this instance's
// history. It creates rows and touches no data.
func (h *BackupHandler) AdoptSet(c *okapi.Context, req *AdoptSetRequest) error {
	inst, err := h.loadInstance(c)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	cfg, base, err := h.bucketFor(inst.WorkspaceID)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	res, err := h.svc.AdoptSet(c.Request().Context(), cfg, base, inst, strings.TrimSpace(req.Body.Ref))
	switch {
	case errors.Is(err, backup.ErrSetNotInBucket):
		return c.AbortNotFound("no recovery point with that ref is in the bucket")
	case errors.Is(err, backup.ErrEngineMismatch):
		return c.AbortBadRequest(err.Error())
	case err != nil:
		return c.AbortInternalServerError("failed to adopt the recovery point", err)
	}
	h.record(c, inst.WorkspaceID, "database.backup_set_adopt", res.SetID)
	return ok(c, res)
}

// RestoreSetRequest carries the restore method for a whole recovery point.
type RestoreSetRequest struct {
	Body struct {
		// "force" drops and recreates each database first; anything else restores
		// over what is there.
		Method string `json:"method" enum:"normal,force"`
		// AllowVersionMismatch loads dumps taken from a newer engine anyway.
		AllowVersionMismatch bool `json:"allow_version_mismatch"`
	} `json:"body"`
}

// RestoreSet restores every database in a recovery point.
func (h *BackupHandler) RestoreSet(c *okapi.Context, req *RestoreSetRequest) error {
	wsID := middlewares.WorkspaceID(c)
	set, err := h.loadSet(c, wsID)
	if err != nil {
		return c.AbortNotFound("backup set not found")
	}
	inst, err := h.dbs.FindInWorkspace(wsID, set.InstanceID)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	dest, err := h.setDestination(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to resolve the backup destination", err)
	}
	res, err := h.svc.RestoreSet(c.Request().Context(), inst, set, dest,
		req.Body.Method == "force", req.Body.AllowVersionMismatch)
	if err != nil {
		if errors.Is(err, backup.ErrNoBackupFile) {
			return c.AbortBadRequest("this recovery point has no artifacts to restore")
		}
		return c.AbortInternalServerError("failed to restore the recovery point", err)
	}
	h.record(c, wsID, "database.backup_set_restore", set.ID)
	return ok(c, res)
}

// bucketFor resolves the workspace's object-storage target, or says why it cannot.
func (h *BackupHandler) bucketFor(workspaceID uint) (*backup.S3Config, string, error) {
	if h.settings == nil {
		return nil, "", errors.New("workspace backup settings are not available")
	}
	cfg, base, err := h.settings.DatabaseBackupTarget(workspaceID)
	if err != nil {
		return nil, "", err
	}
	if cfg == nil {
		return nil, "", errors.New("recovery points need the workspace S3 backup target — configure it under Workspace settings → Backups")
	}
	return cfg, base, nil
}

// VerifySet re-checks a recovery point against the bucket on demand.
func (h *BackupHandler) VerifySet(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	set, err := h.loadSet(c, wsID)
	if err != nil {
		return c.AbortNotFound("backup set not found")
	}
	cfg, _, err := h.bucketFor(wsID)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	pass, _ := h.settings.DatabaseBackupPassphrase(wsID)
	res, err := h.svc.VerifySet(c.Request().Context(), cfg, set, pass)
	if err != nil {
		return c.AbortInternalServerError("failed to verify the recovery point", err)
	}
	h.record(c, wsID, "database.backup_set_verify", set.ID)
	return ok(c, res)
}

// RecoveryKit downloads the instructions for reading a recovery point back without
// Miabi. The kit carries the sealed envelope but never the data key, so it is only
// useful to someone who also has the passphrase.
func (h *BackupHandler) RecoveryKit(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	set, err := h.loadSet(c, wsID)
	if err != nil {
		return c.AbortNotFound("backup set not found")
	}
	filename, body, err := h.svc.RecoveryKit(set)
	if err != nil {
		return c.AbortInternalServerError("failed to build the recovery kit", err)
	}
	h.record(c, wsID, "database.backup_set_recovery_kit", set.ID)
	c.SetHeader("Content-Type", "text/markdown; charset=utf-8")
	c.SetHeader("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.SetHeader("Content-Length", strconv.Itoa(len(body)))
	w := c.Response()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}

// SetScheduleRequest creates or replaces an instance's recovery-point schedule.
type SetScheduleRequest struct {
	Body struct {
		Cron string `json:"cron" required:"true"`
		// MaxSets keeps at most N recovery points; RetentionDays deletes any older
		// than N days. Zero means unbounded. The newest completed set always
		// survives regardless.
		MaxSets       int  `json:"max_sets"`
		RetentionDays int  `json:"retention_days"`
		Concurrency   int  `json:"concurrency"`
		Enabled       bool `json:"enabled"`
	} `json:"body"`
}

// ListSetSchedules returns an instance's recovery-point schedules.
func (h *BackupHandler) ListSetSchedules(c *okapi.Context) error {
	inst, err := h.loadInstance(c)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	out, err := h.sets.ListSchedulesByInstance(inst.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list schedules", err)
	}
	return ok(c, out)
}

// CreateSetSchedule schedules recovery points for the instance.
func (h *BackupHandler) CreateSetSchedule(c *okapi.Context, req *SetScheduleRequest) error {
	inst, err := h.loadInstance(c)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	// Refuse a schedule that could only ever fail, rather than letting it discover
	// that at 03:00 every night.
	if !h.s3Configured(inst.WorkspaceID) {
		return c.AbortBadRequest("recovery points need the workspace S3 backup target — configure it under Workspace settings → Backups")
	}
	sched := &models.DatabaseBackupSetSchedule{
		WorkspaceID:   inst.WorkspaceID,
		InstanceID:    inst.ID,
		Cron:          strings.TrimSpace(req.Body.Cron),
		Enabled:       req.Body.Enabled,
		MaxSets:       req.Body.MaxSets,
		RetentionDays: req.Body.RetentionDays,
		Concurrency:   req.Body.Concurrency,
	}
	if err := h.sets.CreateSchedule(sched); err != nil {
		return c.AbortInternalServerError("failed to create the schedule", err)
	}
	if h.cron != nil && sched.Enabled {
		h.cron.RegisterSet(*sched)
	}
	h.record(c, inst.WorkspaceID, "database.backup_set_schedule_create", sched.ID)
	return ok(c, sched)
}

// DeleteSetSchedule removes a recovery-point schedule.
func (h *BackupHandler) DeleteSetSchedule(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	id, err := strconv.Atoi(c.Param("scheduleID"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid schedule id")
	}
	sched, err := h.sets.FindScheduleInWorkspace(wsID, uint(id))
	if err != nil {
		return c.AbortNotFound("schedule not found")
	}
	if err := h.sets.DeleteSchedule(sched.ID); err != nil {
		return c.AbortInternalServerError("failed to delete the schedule", err)
	}
	if h.cron != nil {
		h.cron.UnregisterSet(sched.ID)
	}
	h.record(c, wsID, "database.backup_set_schedule_delete", sched.ID)
	return ok(c, okapi.M{"deleted": true})
}
