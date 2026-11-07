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

// Viewer lists keys and digests; Developer mutates; Admin reveals content, mirroring the Vault.
func (r *Router) configRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Configs", Description: "Workspace configuration files, mounted into applications as read-only files."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/configs"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.config.List,
			Summary:     "List configs (keys and digests, no content)",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.config.Create),
			Summary:     "Create a config",
			Request:     &handlers.CreateConfigRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{configID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.config.Get,
			Summary:     "Get a config (keys and digests, no content)",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{configID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.config.Update),
			Summary:     "Update a config's files",
			Request:     &handlers.UpdateConfigRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{configID}/reveal",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.config.Reveal,
			Summary:     "Reveal a config's file content (admin)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{configID}/usage",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.config.Usage,
			Summary:     "List apps mounting a config",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{configID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.config.Delete,
			Summary:     "Delete a config",
		},
	}
}
