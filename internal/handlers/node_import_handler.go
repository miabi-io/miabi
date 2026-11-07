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
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/dockerimport"
)

// Scanning is system-admin because it reads the raw node Docker surface.

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
		// Fallback only: per-item stack_name wins, so each compose project maps to its own stack.
		StackName string `json:"stack_name"`
		Items     []struct {
			Kind      string `json:"kind" required:"true" enum:"container,volume,network"`
			Ref       string `json:"ref" required:"true"`
			AppName   string `json:"app_name"`
			Mode      string `json:"mode" enum:"adopt,reconcile"`
			StackName string `json:"stack_name"`
		} `json:"items" required:"true"`
	} `json:"body"`
}

// Non-destructive by default (adopt-in-place); per-item "reconcile" recreates natively.
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
		if a := placementAbort(c, err); a != nil {
			return a
		}
		return c.AbortInternalServerError("import failed", err)
	}
	h.record(c, "node.import", id)
	return ok(c, out)
}
