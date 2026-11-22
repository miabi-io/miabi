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
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/registryserver"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type RegistryRetentionHandler struct {
	store  *repositories.RegistryRetentionRepository
	runner *registryserver.RetentionRunner
	audit  *audit.Logger
}

func NewRegistryRetentionHandler(store *repositories.RegistryRetentionRepository, runner *registryserver.RetentionRunner, auditLog *audit.Logger) *RegistryRetentionHandler {
	return &RegistryRetentionHandler{store: store, runner: runner, audit: auditLog}
}

func (h *RegistryRetentionHandler) GetPolicy(c *okapi.Context) error {
	p, err := h.store.Policy()
	if err != nil {
		return c.AbortInternalServerError("failed to load the retention policy", err)
	}
	return ok(c, p)
}

type UpdateRetentionPolicyRequest struct {
	Body struct {
		Enabled       bool     `json:"enabled"`
		KeepImages    int      `json:"keep_images" min:"1" max:"1000"`
		MinAgeDays    int      `json:"min_age_days" min:"0" max:"3650"`
		ReleaseWindow int      `json:"release_window" min:"0" max:"100"`
		ProtectedTags []string `json:"protected_tags"`
	} `json:"body"`
}

func (h *RegistryRetentionHandler) UpdatePolicy(c *okapi.Context, req *UpdateRetentionPolicyRequest) error {
	b := req.Body
	if b.KeepImages < 1 {
		return c.AbortBadRequest("keep_images must be at least 1")
	}
	patterns, err := cleanPatterns(b.ProtectedTags)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	p := &models.RegistryRetention{
		Enabled: b.Enabled, KeepImages: b.KeepImages, MinAgeDays: b.MinAgeDays,
		ReleaseWindow: b.ReleaseWindow, ProtectedTags: patterns,
	}
	if err := h.store.SavePolicy(p); err != nil {
		return c.AbortInternalServerError("failed to save the retention policy", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: "registry.retention_policy", TargetType: "registry", IP: c.RealIP(),
		Metadata: map[string]any{"enabled": p.Enabled, "keep_images": p.KeepImages, "min_age_days": p.MinAgeDays,
			"release_window": p.ReleaseWindow, "protected_tags": p.ProtectedTags},
	})
	return ok(c, p)
}

var errBadPattern = errors.New("protected tag patterns must be valid globs, at most 20 of them")

func cleanPatterns(in []string) ([]string, error) {
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := path.Match(p, ""); err != nil {
			return nil, errBadPattern
		}
		out = append(out, p)
	}
	if len(out) > 20 {
		return nil, errBadPattern
	}
	return out, nil
}

type StartRetentionRequest struct {
	Body struct {
		// DryRun previews what the policy would delete, whether or not it is enabled.
		DryRun bool `json:"dry_run"`
	} `json:"body"`
}

// StartRun answers at once with the run; its report is filled in as it finishes.
func (h *RegistryRetentionHandler) StartRun(c *okapi.Context, req *StartRetentionRequest) error {
	run, err := h.runner.Start(req.Body.DryRun, "manual")
	if errors.Is(err, registryserver.ErrRetentionRunning) {
		return c.AbortWithError(http.StatusConflict, err)
	}
	if err != nil {
		return c.AbortInternalServerError("failed to start the retention run", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: "registry.retention_run", TargetType: "registry", TargetID: strconv.FormatUint(uint64(run.ID), 10),
		IP: c.RealIP(), Metadata: map[string]any{"dry_run": run.DryRun},
	})
	return created(c, run)
}

func (h *RegistryRetentionHandler) Runs(c *okapi.Context) error {
	runs, err := h.store.Runs(20)
	if err != nil {
		return c.AbortInternalServerError("failed to list retention runs", err)
	}
	return ok(c, runs)
}

type RetentionRunDetail struct {
	models.RegistryRetentionRun
	Workspaces []registryserver.RetentionWorkspaceReport `json:"workspaces"`
}

func (h *RegistryRetentionHandler) Run(c *okapi.Context) error {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		return c.AbortBadRequest("invalid run id")
	}
	run, err := h.store.Run(uint(id))
	if err != nil {
		return c.AbortNotFound("retention run not found")
	}
	out := RetentionRunDetail{RegistryRetentionRun: *run, Workspaces: []registryserver.RetentionWorkspaceReport{}}
	if run.Report != "" {
		_ = json.Unmarshal([]byte(run.Report), &out.Workspaces)
	}
	return ok(c, out)
}

// WorkspaceRetention is what a workspace sees: the platform's rule and its own choice.
type WorkspaceRetention struct {
	Platform *models.RegistryRetention         `json:"platform"`
	Override *models.RegistryRetentionOverride `json:"override"`
}

func (h *RegistryRetentionHandler) GetWorkspace(c *okapi.Context) error {
	p, err := h.store.Policy()
	if err != nil {
		return c.AbortInternalServerError("failed to load the retention policy", err)
	}
	o, err := h.store.Override(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to load the workspace's retention choice", err)
	}
	return ok(c, WorkspaceRetention{Platform: p, Override: o})
}

type UpdateWorkspaceRetentionRequest struct {
	Body struct {
		Disabled bool `json:"disabled"`
		// KeepImages 0 follows the platform; below the platform's count it has no effect.
		KeepImages int `json:"keep_images" min:"0" max:"1000"`
	} `json:"body"`
}

func (h *RegistryRetentionHandler) UpdateWorkspace(c *okapi.Context, req *UpdateWorkspaceRetentionRequest) error {
	wsID := middlewares.WorkspaceID(c)
	o := &models.RegistryRetentionOverride{WorkspaceID: wsID, Disabled: req.Body.Disabled, KeepImages: req.Body.KeepImages}
	if err := h.store.SaveOverride(o); err != nil {
		return c.AbortInternalServerError("failed to save the workspace's retention choice", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, WorkspaceID: &wsID, Action: "registry.retention_override", TargetType: "workspace",
		TargetID: strconv.FormatUint(uint64(wsID), 10), IP: c.RealIP(),
		Metadata: map[string]any{"disabled": o.Disabled, "keep_images": o.KeepImages},
	})
	return ok(c, o)
}
