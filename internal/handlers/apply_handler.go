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
	"fmt"
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/apply"
	"github.com/miabi-io/miabi/internal/services/audit"
)

type ApplyHandler struct {
	svc *apply.Service
	// The route addresses an app by id or uid, while the apply service works in manifest names.
	apps *application.Service
	// placer tells a platform admin's apply apart, which may create in restricted locations.
	placer *Placer
	audit  *audit.Logger
}

func NewApplyHandler(svc *apply.Service, apps *application.Service, placer *Placer, auditLog *audit.Logger) *ApplyHandler {
	return &ApplyHandler{svc: svc, apps: apps, placer: placer, audit: auditLog}
}

// Returns raw YAML, not the JSON envelope; refused for marketplace or GitOps-owned apps.
func (h *ApplyHandler) ExportApplication(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	id, err := appRef(c, h.apps.IDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid app id")
	}
	app, err := h.apps.Get(wsID, id)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if owner, owned := models.SourceOwnedElsewhere(app.Metadata); owned {
		return c.AbortWithError(http.StatusConflict, fmt.Errorf(
			"this application is managed by %s, so its manifest is owned there — exporting a second copy "+
				"would leave two documents claiming the same app", owner))
	}
	includeJobs := c.Query("include_jobs") == "true"
	bundle, err := h.svc.ExportApplication(c.Request().Context(), wsID, app.Name, includeJobs)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.audit.Record(audit.Entry{
		ActorID: ptr(middlewares.UserID(c)), WorkspaceID: &wsID, Action: "application.export",
		TargetType: "application", TargetID: strconv.Itoa(int(app.ID)), IP: c.RealIP(),
	})
	c.SetHeader("Content-Disposition", `attachment; filename="`+app.Name+`.yaml"`)
	return c.Data(http.StatusOK, "application/yaml", bundle)
}

func ptr[T any](v T) *T { return &v }

// ApplyRequest carries the manifest bundle and apply options.
type ApplyRequest struct {
	Body struct {
		// Manifests is a single- or multi-document miabi.io/v1 YAML bundle.
		Manifests string `json:"manifests" required:"true"`
		// Prune deletes managed resources absent from the bundle (opt-in).
		Prune bool `json:"prune"`
		// DryRun returns the plan without applying it.
		DryRun bool `json:"dry_run"`
		Delete bool `json:"delete"`
		// Force applies changes to resources a GitOps project owns; its next sync reverts them.
		Force bool `json:"force"`
	} `json:"body"`
}

// Apply previews or executes a declarative bundle.
func (h *ApplyHandler) Apply(c *okapi.Context, req *ApplyRequest) error {
	wsID := middlewares.WorkspaceID(c)
	ctx := c.Request().Context()

	// Delete mode: remove exactly the resources the bundle names (inverse of apply).
	if req.Body.Delete {
		res, err := h.svc.Delete(ctx, wsID, []byte(req.Body.Manifests), req.Body.DryRun)
		if err != nil {
			return h.mapErr(c, err)
		}
		if req.Body.DryRun {
			return ok(c, res.Plan)
		}
		actor := middlewares.UserID(c)
		h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "apply.delete",
			TargetType: "workspace", TargetID: strconv.Itoa(int(wsID)), IP: c.RealIP()})
		return ok(c, res)
	}

	opts := apply.Options{Prune: req.Body.Prune, Force: req.Body.Force, Admin: h.placer.admin(c)}
	if req.Body.DryRun {
		opts.CheckReferences = true
		plan, _, err := h.svc.Plan(ctx, wsID, []byte(req.Body.Manifests), opts)
		if err != nil {
			return h.mapErr(c, err)
		}
		return ok(c, plan)
	}

	res, err := h.svc.Apply(ctx, wsID, []byte(req.Body.Manifests), opts)
	if err != nil {
		return h.mapErr(c, err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "apply.run",
		TargetType: "workspace", TargetID: strconv.Itoa(int(wsID)), IP: c.RealIP()})
	return ok(c, res)
}

func (h *ApplyHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, apply.ErrInvalidManifest):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, apply.ErrOwnedByGitOps):
		return c.AbortWithError(http.StatusConflict, err)
	default:
		return c.AbortInternalServerError("apply failed", err)
	}
}
