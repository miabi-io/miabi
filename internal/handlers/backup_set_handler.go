// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

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
	return ok(c, sets)
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
