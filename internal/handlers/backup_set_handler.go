// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
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
