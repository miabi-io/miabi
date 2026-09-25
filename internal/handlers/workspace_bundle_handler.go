// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/wsbackup"
)

// WorkspaceBundleHandler exposes portable workspace backup & restore: exporting
// a workspace to the bucket as a bundle, listing what the bucket holds, and
// restoring one back.
type WorkspaceBundleHandler struct {
	svc   *wsbackup.Service
	audit *audit.Logger
}

func NewWorkspaceBundleHandler(svc *wsbackup.Service, auditLog *audit.Logger) *WorkspaceBundleHandler {
	return &WorkspaceBundleHandler{svc: svc, audit: auditLog}
}

// Status reports whether the workspace can take a bundle, so the UI can explain
// what is missing instead of offering an action that fails.
func (h *WorkspaceBundleHandler) Status(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	encrypted, err := h.svc.Configured(wsID)
	res := map[string]any{"configured": err == nil, "encrypted": encrypted}
	if err != nil {
		res["reason"] = err.Error()
	}
	return ok(c, res)
}

// Runs returns this workspace's export/restore history.
func (h *WorkspaceBundleHandler) Runs(c *okapi.Context) error {
	runs, err := h.svc.List(middlewares.WorkspaceID(c), 50)
	if err != nil {
		return c.AbortInternalServerError("failed to list bundle runs", err)
	}
	return ok(c, runs)
}

// Run returns one export/restore run, with its report.
func (h *WorkspaceBundleHandler) Run(c *okapi.Context) error {
	id, err := strconv.ParseUint(c.Param("runID"), 10, 64)
	if err != nil {
		return c.AbortBadRequest("invalid run id")
	}
	run, err := h.svc.Get(middlewares.WorkspaceID(c), uint(id))
	if err != nil {
		return c.AbortNotFound("bundle run not found")
	}
	return ok(c, run)
}

// DeleteRun removes a run record. The bundle in the bucket is untouched.
func (h *WorkspaceBundleHandler) DeleteRun(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	id, err := strconv.ParseUint(c.Param("runID"), 10, 64)
	if err != nil {
		return c.AbortBadRequest("invalid run id")
	}
	if err := h.svc.Delete(wsID, uint(id)); err != nil {
		return c.AbortNotFound("bundle run not found")
	}
	h.record(c, wsID, "workspace.bundle_run_delete", c.Param("runID"))
	return message(c, "bundle run deleted")
}

// Bundles lists what the bucket actually holds — the list a restore is chosen
// from, read from the bundles' own info files rather than from this platform's
// memory of writing them.
func (h *WorkspaceBundleHandler) Bundles(c *okapi.Context) error {
	list, err := h.svc.Bundles(c.Request().Context(), middlewares.WorkspaceID(c))
	if err != nil {
		return bundleError(c, err, "failed to list bundles in the bucket")
	}
	return ok(c, list)
}

// Bundle returns one bundle's info file.
func (h *WorkspaceBundleHandler) Bundle(c *okapi.Context) error {
	info, err := h.svc.FindBundle(c.Request().Context(), middlewares.WorkspaceID(c), c.Param("ref"))
	if err != nil {
		return bundleError(c, err, "failed to read the bundle")
	}
	return ok(c, info)
}

// DeleteBundle removes a bundle from the bucket: its index, its state file and
// every artifact under it.
func (h *WorkspaceBundleHandler) DeleteBundle(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	ref := c.Param("ref")
	if err := h.svc.DeleteBundle(c.Request().Context(), wsID, ref); err != nil {
		return bundleError(c, err, "failed to delete the bundle")
	}
	h.record(c, wsID, "workspace.bundle_delete", ref)
	return message(c, "bundle deleted")
}

// Export starts an export of this workspace to the bucket.
func (h *WorkspaceBundleHandler) Export(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	userID := middlewares.UserID(c)
	run, err := h.svc.Export(c.Request().Context(), wsID, &userID, "manual")
	if err != nil {
		return bundleError(c, err, "failed to start the export")
	}
	h.record(c, wsID, "workspace.bundle_export", run.Ref)
	return ok(c, run)
}

// RestoreBundleRequest is the body for starting a restore.
type RestoreBundleRequest struct {
	Body struct {
		// Ref names the bundle in the bucket.
		Ref string `json:"ref"`
		// NewWorkspace, when set, restores into a workspace created with that name
		// instead of into this one — a clone rather than an overwrite.
		NewWorkspace string `json:"new_workspace"`
		// RestoreData pulls the database dumps and volume archives too.
		RestoreData bool `json:"restore_data"`
		// DeployApps rolls the restored applications out at the end.
		DeployApps bool `json:"deploy_apps"`
	} `json:"body"`
}

// Restore starts a restore from a bundle in the bucket.
func (h *WorkspaceBundleHandler) Restore(c *okapi.Context, req *RestoreBundleRequest) error {
	wsID := middlewares.WorkspaceID(c)
	userID := middlewares.UserID(c)
	if req.Body.Ref == "" {
		return c.AbortBadRequest("a bundle reference is required")
	}
	run, err := h.svc.Restore(c.Request().Context(), wsID, &userID, wsbackup.RestoreInput{
		Ref:              req.Body.Ref,
		NewWorkspaceName: req.Body.NewWorkspace,
		RestoreData:      req.Body.RestoreData,
		DeployApps:       req.Body.DeployApps,
	})
	if err != nil {
		return bundleError(c, err, "failed to start the restore")
	}
	h.record(c, wsID, "workspace.bundle_restore", run.Ref)
	return ok(c, run)
}

// bundleError maps the service's sentinel errors onto the response envelope, so
// a missing passphrase reads as a configuration problem the operator can fix and
// not as an internal failure.
func bundleError(c *okapi.Context, err error, fallback string) error {
	switch {
	case errors.Is(err, wsbackup.ErrNotConfigured):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, wsbackup.ErrBusy):
		return c.AbortWithError(409, err)
	case errors.Is(err, wsbackup.ErrNotFound):
		return c.AbortNotFound(err.Error())
	default:
		return c.AbortInternalServerError(fallback, err)
	}
}

func (h *WorkspaceBundleHandler) record(c *okapi.Context, wsID uint, action, target string) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID:     &actor,
		WorkspaceID: &wsID,
		Action:      action,
		TargetType:  "workspace_bundle",
		TargetID:    target,
		IP:          c.RealIP(),
	})
}
