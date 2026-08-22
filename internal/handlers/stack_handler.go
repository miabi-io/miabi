// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/stack"
)

type StackHandler struct {
	svc   *stack.Service
	audit *audit.Logger
}

func NewStackHandler(svc *stack.Service, auditLog *audit.Logger) *StackHandler {
	return &StackHandler{svc: svc, audit: auditLog}
}

type CreateStackRequest struct {
	Body struct {
		Name        string `json:"name" required:"true"` // desired unique slug handle
		DisplayName string `json:"display_name"`         // free-text label (defaults to name)
		Description string `json:"description"`
	} `json:"body"`
}

type UpdateStackRequest struct {
	Body struct {
		// Name is immutable. It is still accepted so a client that PATCHes the whole
		// object back unchanged keeps working; sending a different one is refused
		// with 409 rather than silently ignored, so a rename never looks like it
		// succeeded.
		Name        *string `json:"name"`
		DisplayName *string `json:"display_name"`
		Description *string `json:"description"`
	} `json:"body"`
}

type SetStackEnvVarRequest struct {
	Body struct {
		Key      string `json:"key" required:"true"`
		Value    string `json:"value"`
		IsSecret bool   `json:"is_secret"`
	} `json:"body"`
}

type ImportStackRequest struct {
	Body struct {
		Name    string `json:"name" required:"true"`
		Compose string `json:"compose" required:"true"`
	} `json:"body"`
}

type stackListItem struct {
	models.Stack
	AppCount int64              `json:"app_count"`
	Status   stack.StatusCounts `json:"status"`
}

func (h *StackHandler) Create(c *okapi.Context, req *CreateStackRequest) error {
	wsID := middlewares.WorkspaceID(c)
	st, err := h.svc.Create(c.Request().Context(), wsID, stack.Input{Name: req.Body.Name, DisplayName: req.Body.DisplayName, Description: req.Body.Description})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack.create", st.ID)
	return created(c, st)
}

func (h *StackHandler) List(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	stacks, err := h.svc.List(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to list stacks", err)
	}
	items := make([]stackListItem, 0, len(stacks))
	for i := range stacks {
		count, _ := h.svc.AppCount(stacks[i].ID)
		status, _ := h.svc.AggregateStatus(stacks[i].ID)
		items = append(items, stackListItem{Stack: stacks[i], AppCount: count, Status: status})
	}
	return ok(c, items)
}

func (h *StackHandler) Get(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	st, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, st)
}

func (h *StackHandler) Update(c *okapi.Context, req *UpdateStackRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	wsID := middlewares.WorkspaceID(c)
	st, err := h.svc.Update(wsID, id, stack.UpdateInput{
		Name:        req.Body.Name,
		DisplayName: req.Body.DisplayName,
		Description: req.Body.Description,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack.update", st.ID)
	return ok(c, st)
}

func (h *StackHandler) Delete(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	wsID := middlewares.WorkspaceID(c)
	withApps := c.Query("with_apps") == "true"
	if err := h.svc.Delete(c.Request().Context(), wsID, id, withApps); err != nil {
		return h.mapErr(c, err)
	}
	action := "stack.delete"
	if withApps {
		action = "stack.delete_with_apps"
	}
	h.record(c, wsID, action, id)
	return message(c, "stack deleted")
}

func (h *StackHandler) AddApp(c *okapi.Context) error {
	stackID, appID, err := h.stackAppIDs(c)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	wsID := middlewares.WorkspaceID(c)
	st, err := h.svc.AddApp(wsID, stackID, appID)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack.app_add", stackID)
	return ok(c, st)
}

func (h *StackHandler) RemoveApp(c *okapi.Context) error {
	stackID, appID, err := h.stackAppIDs(c)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	wsID := middlewares.WorkspaceID(c)
	st, err := h.svc.RemoveApp(wsID, stackID, appID)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack.app_remove", stackID)
	return ok(c, st)
}

func (h *StackHandler) Start(c *okapi.Context) error   { return h.lifecycle(c, stack.ActionStart) }
func (h *StackHandler) Stop(c *okapi.Context) error    { return h.lifecycle(c, stack.ActionStop) }
func (h *StackHandler) Restart(c *okapi.Context) error { return h.lifecycle(c, stack.ActionRestart) }

// lifecycle runs a start/stop/restart across the stack's apps and returns the
// per-app result summary. Restart honors ?rolling=true (one app at a time with
// a readiness gate).
func (h *StackHandler) lifecycle(c *okapi.Context, action stack.Action) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	wsID := middlewares.WorkspaceID(c)
	rolling := action == stack.ActionRestart && c.Query("rolling") == "true"
	results, err := h.svc.Lifecycle(c.Request().Context(), wsID, id, action, rolling)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack."+string(action), id)
	return ok(c, results)
}

// DeployAll enqueues a deploy for every app in the stack.
func (h *StackHandler) DeployAll(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	wsID := middlewares.WorkspaceID(c)
	results, err := h.svc.DeployAll(wsID, id)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack.deploy", id)
	return ok(c, results)
}

// Events returns the stack's combined application activity feed.
func (h *StackHandler) Events(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	events, err := h.svc.Events(middlewares.WorkspaceID(c), id, 30)
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, events)
}

// ListEnvVars returns the stack's shared environment variables.
func (h *StackHandler) ListEnvVars(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	vars, err := h.svc.ListEnvVars(middlewares.WorkspaceID(c), id)
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, vars)
}

// SetEnvVar upserts a shared environment variable on the stack.
func (h *StackHandler) SetEnvVar(c *okapi.Context, req *SetStackEnvVarRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.SetEnvVar(wsID, id, req.Body.Key, req.Body.Value, req.Body.IsSecret); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack.env_set", id)
	return message(c, "environment variable set")
}

// ImportEnvVars bulk-upserts the stack's shared env vars from a .env block.
func (h *StackHandler) ImportEnvVars(c *okapi.Context, req *ImportEnvVarsRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	wsID := middlewares.WorkspaceID(c)
	n, err := h.svc.ImportEnvVars(wsID, id, req.Body.Content, req.Body.IsSecret)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack.env_import", id)
	return ok(c, map[string]any{"imported": n})
}

// DeleteEnvVar removes a shared environment variable from the stack.
func (h *StackHandler) DeleteEnvVar(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid stack id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.DeleteEnvVar(wsID, id, c.Param("key")); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack.env_delete", id)
	return message(c, "environment variable removed")
}

// Import creates a stack and its apps from a docker-compose file.
func (h *StackHandler) Import(c *okapi.Context, req *ImportStackRequest) error {
	wsID := middlewares.WorkspaceID(c)
	result, err := h.svc.ImportCompose(c.Request().Context(), wsID, middlewares.UserID(c), req.Body.Name, req.Body.Compose)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "stack.import", result.Stack.ID)
	return created(c, result)
}

func (h *StackHandler) id(c *okapi.Context) (uint, error) {
	id, err := resolveID(c.Param("stackID"), h.svc.IDByUID)
	if err != nil {
		return 0, errors.New("invalid stack id")
	}
	return id, nil
}

func (h *StackHandler) stackAppIDs(c *okapi.Context) (uint, uint, error) {
	stackID, err := h.id(c)
	if err != nil {
		return 0, 0, err
	}
	appID, err := strconv.Atoi(c.Param("appID"))
	if err != nil || appID <= 0 {
		return 0, 0, errors.New("invalid app id")
	}
	return stackID, uint(appID), nil
}

func (h *StackHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "stack", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *StackHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, stack.ErrNameTaken), errors.Is(err, stack.ErrAppInOtherStack),
		errors.Is(err, stack.ErrNameImmutable):
		return c.AbortWithError(409, err)
	case errors.Is(err, stack.ErrNameRequired), errors.Is(err, stack.ErrKeyRequired), errors.Is(err, stack.ErrComposeInvalid):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, stack.ErrNotFound):
		return c.AbortNotFound("stack not found")
	case errors.Is(err, stack.ErrAppNotFound):
		return c.AbortNotFound("application not found")
	default:
		return c.AbortInternalServerError("stack operation failed", err)
	}
}
