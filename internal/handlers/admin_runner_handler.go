// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/runner"
)

// AdminRunnerHandler exposes platform-admin management of the shared runner pool
// (WorkspaceID = nil, Scope = shared): runners any workspace with the
// platform-runners capability may use. Workspace members can *use* a shared
// runner but only admins edit it — the read-all / edit-admin pattern used for
// global TemplateSource/OAuthProvider. Reuses RunnerHandler.mapErr for envelope
// mapping.
type AdminRunnerHandler struct {
	svc   *runner.Service
	base  *RunnerHandler
	ee    enterprise.EE
	audit *audit.Logger
}

func NewAdminRunnerHandler(svc *runner.Service, ee enterprise.EE, auditLog *audit.Logger) *AdminRunnerHandler {
	return &AdminRunnerHandler{svc: svc, base: NewRunnerHandler(svc, auditLog), ee: ee, audit: auditLog}
}

func (h *AdminRunnerHandler) requireCreateCapacity(c *okapi.Context) error {
	if h.ee.Mutable(enterprise.FlagPlatformRunners) {
		return nil // unlimited shared pool
	}
	if enterprise.CommunityRunnerLimit < 0 {
		return nil
	}
	runners, err := h.svc.ListShared()
	if err != nil {
		return c.AbortInternalServerError("failed to count runners", err)
	}
	if len(runners) >= enterprise.CommunityRunnerLimit {
		return entitlementAbort(c, enterprise.ErrRunnerLimitReached)
	}
	return nil
}

// SetConnRegistry wires the live-tunnel lookup used to annotate the shared
// runners' Connected flag (nil-safe).
func (h *AdminRunnerHandler) SetConnRegistry(c RunnerConnRegistry) { h.base.SetConnRegistry(c) }

// List returns the shared runner pool.
func (h *AdminRunnerHandler) List(c *okapi.Context) error {
	runners, err := h.svc.ListShared()
	if err != nil {
		return c.AbortInternalServerError("failed to list runners", err)
	}
	h.base.annotate(runners)
	return ok(c, runners)
}

// Get returns one shared runner.
func (h *AdminRunnerHandler) Get(c *okapi.Context) error {
	r, err := h.svc.GetShared(h.id(c))
	if err != nil {
		return h.base.mapErr(c, err)
	}
	h.base.annotateOne(r)
	return ok(c, r)
}

// Create registers a platform-shared runner and returns the one-time token.
func (h *AdminRunnerHandler) Create(c *okapi.Context, req *RunnerRequest) error {
	if err := h.requireCreateCapacity(c); err != nil {
		return err
	}
	r, token, err := h.svc.CreateShared(middlewares.UserID(c), req.input())
	if err != nil {
		return h.base.mapErr(c, err)
	}
	h.record(c, "runner.create", r.ID)
	return created(c, map[string]any{"runner": r, "token": token, "image": h.svc.Image()})
}

// Update edits a shared runner's mutable fields. Not license-gated — managing a
// runner already within the pool's allowance is always permitted.
func (h *AdminRunnerHandler) Update(c *okapi.Context, req *RunnerRequest) error {
	r, err := h.svc.UpdateShared(h.id(c), req.input())
	if err != nil {
		return h.base.mapErr(c, err)
	}
	h.record(c, "runner.update", r.ID)
	return ok(c, r)
}

// Cordon holds a shared runner out of scheduling (or releases it).
func (h *AdminRunnerHandler) Cordon(c *okapi.Context, req *RunnerCordonRequest) error {
	r, err := h.svc.GetShared(h.id(c))
	if err != nil {
		return h.base.mapErr(c, err)
	}
	if err := h.svc.SetCordoned(r, req.Body.Cordoned); err != nil {
		return c.AbortInternalServerError("failed to update runner", err)
	}
	h.record(c, "runner.cordon", r.ID)
	return ok(c, r)
}

// RegenerateToken issues a fresh registration token for a shared runner. Not
// license-gated — it manages an existing runner, it does not grow the pool.
func (h *AdminRunnerHandler) RegenerateToken(c *okapi.Context) error {
	token, err := h.svc.RegenerateTokenShared(h.id(c))
	if err != nil {
		return h.base.mapErr(c, err)
	}
	h.record(c, "runner.token", h.id(c))
	return ok(c, map[string]any{"token": token})
}

// Delete removes a shared runner.
func (h *AdminRunnerHandler) Delete(c *okapi.Context) error {
	if err := h.svc.DeleteShared(h.id(c)); err != nil {
		return h.base.mapErr(c, err)
	}
	h.record(c, "runner.delete", h.id(c))
	return message(c, "runner deleted")
}

func (h *AdminRunnerHandler) id(c *okapi.Context) uint {
	id, _ := strconv.Atoi(c.Param("runnerID"))
	return uint(id)
}

func (h *AdminRunnerHandler) record(c *okapi.Context, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: action, TargetType: "runner", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
