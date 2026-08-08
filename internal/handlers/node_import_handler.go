// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/dockerimport"
)

// Import existing Docker resources (hand-run containers, compose stacks, volumes, networks) into
// Miabi. Scanning is system-admin because it reads the raw node Docker surface; imported
// resources are assigned to the chosen workspace.

// ImportableResources lists the node's unmanaged (importable) Docker resources,
// enriched with inspect data, compose grouping, relationships, and
// already-imported flags.
func (h *NodeHandler) ImportableResources(c *okapi.Context) error {
	if h.importer == nil {
		return c.AbortInternalServerError("import service unavailable", nil)
	}
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	out, err := h.importer.Discover(c.Request().Context(), id)
	if err != nil {
		return c.AbortInternalServerError("failed to discover importable resources", err)
	}
	return ok(c, out)
}

// ImportResourcesRequest is the selection of resources to import.
type ImportResourcesRequest struct {
	Body struct {
		// WorkspaceID is the workspace the imported resources are assigned to.
		WorkspaceID uint `json:"workspace_id" required:"true"`
		// StackName is a fallback stack for container items with no per-item
		// stack_name (e.g. ungrouped containers). Per-item stack_name wins, so each
		// compose project maps to its own stack.
		StackName string `json:"stack_name"`
		Items     []struct {
			Kind    string `json:"kind" required:"true" enum:"container,volume,network"`
			Ref     string `json:"ref" required:"true"`
			AppName string `json:"app_name"`
			Mode    string `json:"mode" enum:"adopt,reconcile"`
			// StackName groups this container under a Stack (typically its compose
			// project), created or reused.
			StackName string `json:"stack_name"`
		} `json:"items" required:"true"`
	} `json:"body"`
}

// Import creates Miabi records for the selected resources. Non-destructive
// by default (adopt-in-place); per-item "reconcile" recreates natively.
func (h *NodeHandler) Import(c *okapi.Context, req *ImportResourcesRequest) error {
	if h.importer == nil {
		return c.AbortInternalServerError("import service unavailable", nil)
	}
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	items := make([]dockerimport.ImportItem, 0, len(req.Body.Items))
	for _, it := range req.Body.Items {
		mode := dockerimport.ImportMode(it.Mode)
		if mode != dockerimport.ModeReconcile {
			mode = dockerimport.ModeAdopt
		}
		items = append(items, dockerimport.ImportItem{Kind: it.Kind, Ref: it.Ref, AppName: it.AppName, Mode: mode, StackName: it.StackName})
	}
	actor := middlewares.UserID(c)
	out, err := h.importer.Import(c.Request().Context(), actor, id, dockerimport.ImportRequest{
		WorkspaceID: req.Body.WorkspaceID, StackName: req.Body.StackName, Items: items,
	})
	if err != nil {
		return c.AbortInternalServerError("import failed", err)
	}
	h.record(c, "node.import", id)
	return ok(c, out)
}
