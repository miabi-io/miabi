// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/locationmigration"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// LocationMigrationHandler moves an application to another location (Enterprise; gated live_migration).
type LocationMigrationHandler struct {
	svc   *locationmigration.Service
	users *repositories.UserRepository
	ee    enterprise.EE
	audit *audit.Logger
}

func NewLocationMigrationHandler(svc *locationmigration.Service, users *repositories.UserRepository, ee enterprise.EE, auditLog *audit.Logger) *LocationMigrationHandler {
	return &LocationMigrationHandler{svc: svc, users: users, ee: ee, audit: auditLog}
}

// MigrationPlanRequest asks what moving an app to a location would do.
type MigrationPlanRequest struct {
	Body struct {
		// Location is the target location's name.
		Location string `json:"location" required:"true"`
		// Databases overrides the default strategy per instance: move, new_instance or existing_instance.
		Databases []locationmigration.DBChoice `json:"databases"`
	} `json:"body"`
}

// StartMigrationRequest starts a move with a confirmed plan.
type StartMigrationRequest struct {
	Body struct {
		Location  string                       `json:"location" required:"true"`
		Databases []locationmigration.DBChoice `json:"databases"`
		// CutoverMode is auto (cut over as soon as the data is copied) or manual (wait for a person).
		CutoverMode string `json:"cutover_mode"`
		// BandwidthKBps caps the copy; 0 is unlimited.
		BandwidthKBps int `json:"bandwidth_kbps"`
	} `json:"body"`
}

func (h *LocationMigrationHandler) admin(c *okapi.Context) bool {
	u, err := h.users.FindByID(middlewares.UserID(c))
	return err == nil && u.IsAdmin()
}

// Plan is the dry run: what would move, how, what blocks it, and what it will change.
func (h *LocationMigrationHandler) Plan(c *okapi.Context, req *MigrationPlanRequest) error {
	if err := h.ee.Require(enterprise.FlagLiveMigration); err != nil {
		return entitlementAbort(c, err)
	}
	appID, err := appIDParam(c)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	plan, err := h.svc.Plan(c.Request().Context(), middlewares.WorkspaceID(c), appID, locationmigration.PlanRequest{
		Location: req.Body.Location, Databases: req.Body.Databases, Admin: h.admin(c),
	})
	if err != nil {
		return migrationAbort(c, err)
	}
	return ok(c, plan)
}

// Start re-plans and, when nothing blocks, starts the move.
func (h *LocationMigrationHandler) Start(c *okapi.Context, req *StartMigrationRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagLiveMigration); err != nil {
		return entitlementAbort(c, err)
	}
	appID, err := appIDParam(c)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	wsID := middlewares.WorkspaceID(c)
	userID := middlewares.UserID(c)
	m, err := h.svc.Start(c.Request().Context(), wsID, appID, locationmigration.StartRequest{
		PlanRequest:   locationmigration.PlanRequest{Location: req.Body.Location, Databases: req.Body.Databases, Admin: h.admin(c)},
		CutoverMode:   req.Body.CutoverMode,
		BandwidthKBps: req.Body.BandwidthKBps,
		StartedBy:     &userID,
	})
	if err != nil {
		return migrationAbort(c, err)
	}
	h.record(c, wsID, "migration.start", m.ID)
	return ok(c, m)
}

// ListForApp returns an application's migrations, newest first.
func (h *LocationMigrationHandler) ListForApp(c *okapi.Context) error {
	appID, err := appIDParam(c)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	list, err := h.svc.ListForApp(appID)
	if err != nil {
		return c.AbortInternalServerError("failed to list migrations", err)
	}
	out := list[:0]
	for _, m := range list {
		if m.WorkspaceID == middlewares.WorkspaceID(c) {
			out = append(out, m)
		}
	}
	return ok(c, out)
}

// List returns the workspace's migrations.
func (h *LocationMigrationHandler) List(c *okapi.Context) error {
	list, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list migrations", err)
	}
	return ok(c, list)
}

// Get returns one migration with its progress and report.
func (h *LocationMigrationHandler) Get(c *okapi.Context) error {
	id, err := migrationID(c)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	m, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return migrationAbort(c, err)
	}
	return ok(c, m)
}

// Events streams a migration's progress over SSE.
func (h *LocationMigrationHandler) Events(c *okapi.Context) error {
	id, err := migrationID(c)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	m, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return migrationAbort(c, err)
	}
	return h.svc.Stream(c.Request().Context(), m, func(e eventbus.Event) error {
		return c.SSESendJSON(e)
	})
}

// Cutover takes the app down and finishes a migration waiting at the cutover point.
func (h *LocationMigrationHandler) Cutover(c *okapi.Context) error {
	return h.control(c, "migration.cutover", h.svc.Cutover)
}

// Cancel stops a migration before the app goes down.
func (h *LocationMigrationHandler) Cancel(c *okapi.Context) error {
	return h.control(c, "migration.cancel", h.svc.Cancel)
}

// Rollback puts the app back at its source location.
func (h *LocationMigrationHandler) Rollback(c *okapi.Context) error {
	return h.control(c, "migration.rollback", h.svc.Rollback)
}

// Finalize deletes the source copy before the grace period ends.
func (h *LocationMigrationHandler) Finalize(c *okapi.Context) error {
	return h.control(c, "migration.finalize", h.svc.Finalize)
}

func (h *LocationMigrationHandler) control(c *okapi.Context, action string, fn func(workspaceID, id uint) (*models.LocationMigration, error)) error {
	if err := h.ee.RequireMutable(enterprise.FlagLiveMigration); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := migrationID(c)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	wsID := middlewares.WorkspaceID(c)
	m, err := fn(wsID, id)
	if err != nil {
		return migrationAbort(c, err)
	}
	h.record(c, wsID, action, m.ID)
	return ok(c, m)
}

// AdminList returns every migration on the platform.
func (h *LocationMigrationHandler) AdminList(c *okapi.Context) error {
	list, err := h.svc.ListAll()
	if err != nil {
		return c.AbortInternalServerError("failed to list migrations", err)
	}
	return ok(c, list)
}

func migrationID(c *okapi.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("migrationID"), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid migration id")
	}
	return uint(id), nil
}

func migrationAbort(c *okapi.Context, err error) error {
	if perr := placementAbort(c, err); perr != nil {
		return perr
	}
	switch {
	case errors.Is(err, locationmigration.ErrNotFound):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, locationmigration.ErrLocationRequired), errors.Is(err, locationmigration.ErrSameLocation):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, locationmigration.ErrBlocked), errors.Is(err, locationmigration.ErrActive),
		errors.Is(err, locationmigration.ErrWrongState), errors.Is(err, locationmigration.ErrPastPointOfNoReturn):
		return c.AbortWithError(http.StatusConflict, err)
	}
	return c.AbortInternalServerError("migration failed", err)
}

func (h *LocationMigrationHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID:     &actor,
		WorkspaceID: &wsID,
		Action:      action,
		TargetType:  "location_migration",
		TargetID:    fmt.Sprint(id),
		IP:          c.RealIP(),
	})
}
