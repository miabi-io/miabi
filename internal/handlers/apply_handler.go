// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// ApplyHandler exposes the declarative apply API: preview (dry run) or converge
// a workspace to a bundle of miabi.io/v1 manifests.
type ApplyHandler struct {
	svc *apply.Service
	// apps resolves the application an export names. The apply service works in manifest names; the
	// route addresses an app the way every other app route does, by id or uid.
	apps  *application.Service
	audit *audit.Logger
}

func NewApplyHandler(svc *apply.Service, apps *application.Service, auditLog *audit.Logger) *ApplyHandler {
	return &ApplyHandler{svc: svc, apps: apps, audit: auditLog}
}

// ExportApplication renders one application as a miabi.io/v1 bundle, for moving an app created in
// the console into Git. The response is the YAML itself, not the JSON envelope: it is meant to be
// saved to a file, and a caller should not have to unwrap a document to do that.
//
// Refused for an application whose source is owned by a marketplace install or by GitOps. The first
// is described by its template, and the second already has a manifest — handing someone a second
// copy invites two documents claiming the same app.
func (h *ApplyHandler) ExportApplication(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	id, err := resolveID(c.Param("appID"), h.apps.IDByUID)
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
	bundle, err := h.svc.ExportApplication(c.Request().Context(), wsID, app.Name)
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
		// Delete removes exactly the resources the bundle names (the inverse of an
		// apply) instead of converging to them. Honors DryRun.
		Delete bool `json:"delete"`
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

	opts := apply.Options{Prune: req.Body.Prune}
	if req.Body.DryRun {
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
	default:
		return c.AbortInternalServerError("apply failed", err)
	}
}
