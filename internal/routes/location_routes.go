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

func (r *Router) locationRoutes() []okapi.RouteDefinition {
	ws := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Locations", Description: "The locations a workspace can place resources in."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/locations",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.location.List,
			Summary:     "List the locations this workspace can place resources in",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/location-nodes",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.location.Nodes,
			Summary:     "List the nodes of a location (query: location; empty = where a create would land)",
		},
		{
			Method:      http.MethodPut,
			Path:        "/{workspace}/default-location",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.location.SetDefault),
			Summary:     "Set the location new resources land in when none is named",
			Request:     &handlers.SetDefaultLocationRequest{},
		},
	}
}
