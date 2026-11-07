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

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
)

// Admin-only including reads: listings name every resource and restores can add members.
func (r *Router) workspaceBundleRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{
		Name:        "Portable Backup",
		Description: "Export a workspace to an encrypted bundle on S3, and restore one back.",
	})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/portable-backup"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base + "/status",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Status,
			Summary:     "Whether portable backup is configured (S3 + passphrase)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/runs",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Runs,
			Summary:     "List export & restore runs",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/runs/{runID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Run,
			Summary:     "Get one run and its report",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/runs/{runID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.DeleteRun,
			Summary:     "Delete a run record (the bundle is kept)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/bundles",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Bundles,
			Summary:     "List the bundles in the bucket",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/bundles/{ref}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Bundle,
			Summary:     "Read one bundle's index",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/bundles/{ref}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.DeleteBundle,
			Summary:     "Delete a bundle from the bucket",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/export",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Export,
			Summary:     "Export this workspace to a bundle",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/restore",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleOwner),
			Handler:     okapi.H(r.h.workspaceBundle.Restore),
			Summary:     "Restore a bundle into this or a new workspace",
			Request:     &handlers.RestoreBundleRequest{},
		},
	}
}
