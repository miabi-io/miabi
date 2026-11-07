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
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
)

func (r *Router) registryServerRoutes() []okapi.RouteDefinition {
	admin := r.v1.Group("/admin").WithTagInfo(okapi.GroupTag{Name: "Admin", Description: "Platform administration."})
	ws := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Workspaces", Description: "Workspaces."})
	adminMw := []okapi.Middleware{r.authenticate, r.systemAdmin}
	scoped := []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleViewer)}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/registry/settings",
			Group:       admin,
			Middlewares: adminMw,
			Handler:     r.h.adminRegistry.GetSettings,
			Summary:     "Get internal registry settings",
		},
		{
			Method:      http.MethodPut,
			Path:        "/registry/settings",
			Group:       admin,
			Middlewares: adminMw,
			Handler:     okapi.H(r.h.adminRegistry.UpdateSettings),
			Summary:     "Update internal registry settings",
			Request:     &handlers.UpdateRegistrySettingsRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/registry/runtime",
			Group:       admin,
			Middlewares: adminMw,
			Handler:     r.h.adminRegistry.Runtime,
			Summary:     "Get the registry container's live state and resource usage",
		},
		{
			Method:      http.MethodPost,
			Path:        "/registry/gc",
			Group:       admin,
			Middlewares: adminMw,
			Handler:     r.h.adminRegistry.RunGC,
			Summary:     "Run registry garbage collection",
			Response:    &dto.Response[dto.MessageData]{},
		},

		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/registry",
			Group:       ws,
			Middlewares: scoped,
			Handler:     r.h.registryServer.Info,
			Summary:     "Workspace registry connection info",
			Response:    &dto.Response[handlers.RegistryInfo]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/registry/repositories",
			Group:       ws,
			Middlewares: scoped,
			Handler:     r.h.registryServer.Repositories,
			Summary:     "List workspace registry repositories (paged)",
		},
		{
			// ?name= rather than a path segment: an image name may contain slashes.
			Method:      http.MethodGet,
			Path:        "/{workspace}/registry/repository",
			Group:       ws,
			Middlewares: scoped,
			Handler:     r.h.registryServer.RepositoryOverview,
			Summary:     "Get a registry repository's overview",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/registry/repository/tags",
			Group:       ws,
			Middlewares: scoped,
			Handler:     r.h.registryServer.RepositoryTags,
			Summary:     "List a registry repository's tags (paged)",
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{workspace}/registry/repositories/{repo}/tags/{tag}",
			Group:       ws,
			Middlewares: []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleDeveloper)},
			Handler:     r.h.registryServer.DeleteTag,
			Summary:     "Delete a registry tag",
			Response:    &dto.Response[dto.MessageData]{},
		},

		// Unauthenticated, gateway-only; hit on every /v2/* request to allow/deny by docker Basic credentials.
		{
			Method:  http.MethodPost,
			Path:    "/internal/registry/auth",
			Handler: r.h.registryServer.Authenticate,
			Tags:    []string{"Internal"},
			Summary: "Registry forwardAuth (gateway-only)",
		},
		{
			// docker login's /v2/ probe is a GET and some forwardAuth setups replay the original method.
			Method:  http.MethodGet,
			Path:    "/internal/registry/auth",
			Handler: r.h.registryServer.Authenticate,
			Tags:    []string{"Internal"},
			Summary: "Registry forwardAuth (gateway-only)",
		},
	}
}
