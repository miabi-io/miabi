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
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/runner"
	"github.com/miabi-io/miabi/internal/services/updatecheck"
)

// nil disables the annotation, so a runner reads as not connected.
type RunnerConnRegistry interface {
	Connected(id uint) bool
}

// The registration token is returned only at create or regenerate: hashed at rest.
type RunnerHandler struct {
	svc      *runner.Service
	conn     RunnerConnRegistry
	releases AgentReleases
	bus      *eventbus.Bus
	audit    *audit.Logger
}

func NewRunnerHandler(svc *runner.Service, auditLog *audit.Logger) *RunnerHandler {
	return &RunnerHandler{svc: svc, audit: auditLog}
}

func (h *RunnerHandler) SetConnRegistry(c RunnerConnRegistry) { h.conn = c }

// SetRunnerReleases wires the newest known runner release, used to flag outdated runners (nil-safe).
func (h *RunnerHandler) SetRunnerReleases(r AgentReleases) { h.releases = r }

// Computed per request: the runner's build and the release move independently.
func (h *RunnerHandler) annotate(runners []models.Runner) {
	latest := h.latestRelease()
	for i := range runners {
		h.annotateWith(&runners[i], latest)
	}
}

func (h *RunnerHandler) annotateOne(r *models.Runner) {
	if r != nil {
		h.annotateWith(r, h.latestRelease())
	}
}

func (h *RunnerHandler) latestRelease() string {
	if h.releases == nil {
		return ""
	}
	return h.releases.LatestVersion()
}

func (h *RunnerHandler) annotateWith(r *models.Runner, latest string) {
	if h.conn != nil {
		r.Connected = h.conn.Connected(r.ID)
	}
	if updatecheck.IsNewer(r.Version, latest) {
		r.LatestVersion = latest
	}
}

// RunnerRequest is the create/update body for a workspace or shared runner.
type RunnerRequest struct {
	Body struct {
		Name        string   `json:"name" required:"true"` // desired unique slug handle
		DisplayName string   `json:"display_name"`         // free-text label (defaults to name)
		Labels      []string `json:"labels"`               // e.g. ["arch=amd64","buildkit"]
		Concurrency int      `json:"concurrency"`          // max simultaneous leases (min 1)
	} `json:"body"`
}

func (r *RunnerRequest) input() runner.Input {
	return runner.Input{
		Name:        r.Body.Name,
		DisplayName: r.Body.DisplayName,
		Labels:      r.Body.Labels,
		Concurrency: r.Body.Concurrency,
	}
}

// RunnerCordonRequest toggles an operator hold (cordon) on a runner.
type RunnerCordonRequest struct {
	Body struct {
		Cordoned bool `json:"cordoned"`
	} `json:"body"`
}

// List returns the workspace's own runners.
func (h *RunnerHandler) List(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	runners, err := h.svc.ListWorkspace(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to list runners", err)
	}
	h.annotate(runners)
	return ok(c, runners)
}

// Narrowed to what the plan offers, so a member never sees a runner their builds would be refused.
func (h *RunnerHandler) ListShared(c *okapi.Context) error {
	runners, err := h.svc.UsableShared(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list platform runners", err)
	}
	h.annotate(runners)
	return ok(c, runners)
}

// Get returns one of the workspace's runners.
func (h *RunnerHandler) Get(c *okapi.Context) error {
	r, err := h.svc.GetWorkspace(middlewares.WorkspaceID(c), h.id(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	h.annotateOne(r)
	return ok(c, r)
}

// Create registers a workspace-owned runner and returns the one-time token.
func (h *RunnerHandler) Create(c *okapi.Context, req *RunnerRequest) error {
	wsID := middlewares.WorkspaceID(c)
	r, token, err := h.svc.CreateWorkspace(wsID, middlewares.UserID(c), req.input())
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, &wsID, "runner.create", r.ID)
	return created(c, map[string]any{"runner": r, "token": token, "image": h.svc.Image()})
}

// Update edits a workspace runner's mutable fields.
func (h *RunnerHandler) Update(c *okapi.Context, req *RunnerRequest) error {
	wsID := middlewares.WorkspaceID(c)
	r, err := h.svc.UpdateWorkspace(wsID, h.id(c), req.input())
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, &wsID, "runner.update", r.ID)
	return ok(c, r)
}

// Cordon holds a workspace runner out of scheduling (or releases it).
func (h *RunnerHandler) Cordon(c *okapi.Context, req *RunnerCordonRequest) error {
	wsID := middlewares.WorkspaceID(c)
	r, err := h.svc.GetWorkspace(wsID, h.id(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	if err := h.svc.SetCordoned(r, req.Body.Cordoned); err != nil {
		return c.AbortInternalServerError("failed to update runner", err)
	}
	h.record(c, &wsID, "runner.cordon", r.ID)
	return ok(c, r)
}

// RegenerateToken issues a fresh registration token for a workspace runner.
func (h *RunnerHandler) RegenerateToken(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	token, err := h.svc.RegenerateTokenWorkspace(wsID, h.id(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, &wsID, "runner.token", h.id(c))
	return ok(c, map[string]any{"token": token})
}

// Delete removes a workspace runner.
func (h *RunnerHandler) Delete(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.DeleteWorkspace(wsID, h.id(c)); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, &wsID, "runner.delete", h.id(c))
	return message(c, "runner deleted")
}

func (h *RunnerHandler) id(c *okapi.Context) uint {
	id, _ := strconv.Atoi(c.Param("runnerID"))
	return uint(id)
}

func (h *RunnerHandler) record(c *okapi.Context, wsID *uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: wsID, Action: action, TargetType: "runner", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *RunnerHandler) mapErr(c *okapi.Context, err error) error {
	if a := quotaAbort(c, err); a != nil {
		return a
	}
	switch {
	case errors.Is(err, runner.ErrNotFound):
		return c.AbortNotFound("runner not found")
	case errors.Is(err, runner.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, runner.ErrNameRequired):
		return c.AbortBadRequest("a runner name is required")
	default:
		return c.AbortInternalServerError("runner operation failed", err)
	}
}
