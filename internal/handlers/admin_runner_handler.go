// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/runner"
)

// AdminRunnerHandler manages the shared runner pool; members may use a shared runner but only admins edit it.
type AdminRunnerHandler struct {
	svc   *runner.Service
	base  *RunnerHandler
	ee    enterprise.EE
	audit *audit.Logger
}

func NewAdminRunnerHandler(svc *runner.Service, ee enterprise.EE, auditLog *audit.Logger) *AdminRunnerHandler {
	return &AdminRunnerHandler{svc: svc, base: NewRunnerHandler(svc, auditLog), ee: ee, audit: auditLog}
}

// Writes no response: entitlementAbort returns nil, so the refusal must be the handler's own return.
func (h *AdminRunnerHandler) overCreateLimit() bool {
	lim := h.ee.Entitlements().SharedRunnerLimit()
	if lim < 0 {
		return false // unlimited shared pool
	}
	runners, err := h.svc.ListShared()
	return err == nil && len(runners) >= lim
}

func (h *AdminRunnerHandler) SetConnRegistry(c RunnerConnRegistry) { h.base.SetConnRegistry(c) }

// SetRunnerReleases wires the newest known runner release (nil-safe).
func (h *AdminRunnerHandler) SetRunnerReleases(r AgentReleases) { h.base.SetRunnerReleases(r) }

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
	if h.overCreateLimit() {
		return entitlementAbort(c, enterprise.ErrRunnerLimitReached)
	}
	r, token, err := h.svc.CreateShared(middlewares.UserID(c), req.input())
	if err != nil {
		return h.base.mapErr(c, err)
	}
	h.record(c, "runner.create", r.ID)
	return created(c, map[string]any{"runner": r, "token": token, "image": h.svc.Image()})
}

// Not license-gated: managing a runner already within the pool's allowance is always permitted.
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

// Not license-gated: it manages an existing runner, it does not grow the pool.
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
	if plans, err := h.svc.BoundBy(h.id(c)); err == nil && len(plans) > 0 {
		return c.AbortWithError(409, fmt.Errorf("the runner is offered by %s; remove it there first", boundByText(plans)))
	}
	if err := h.svc.DeleteShared(h.id(c)); err != nil {
		return h.base.mapErr(c, err)
	}
	h.record(c, "runner.delete", h.id(c))
	return message(c, "runner deleted")
}

// boundByText names the plans blocking a delete, so the operator knows where to look.
func boundByText(plans []models.Plan) string {
	names := make([]string, 0, len(plans))
	for _, p := range plans {
		names = append(names, strconv.Quote(p.Name))
	}
	if len(names) == 1 {
		return "plan " + names[0]
	}
	return "plans " + strings.Join(names, ", ")
}

func (h *AdminRunnerHandler) id(c *okapi.Context) uint {
	id, _ := strconv.Atoi(c.Param("runnerID"))
	return uint(id)
}

func (h *AdminRunnerHandler) record(c *okapi.Context, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: action, TargetType: "runner", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
