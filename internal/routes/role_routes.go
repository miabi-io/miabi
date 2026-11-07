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

// Writes need member:write plus the custom_roles entitlement (402 in Community).
func (r *Router) roleRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Roles", Description: "Custom roles (permission sets) and member assignment."})
	read := []okapi.Middleware{r.authenticate, r.scope}
	write := []okapi.Middleware{r.authenticate, r.scope, middlewares.RequirePermission(models.PermMemberWrite)}
	const base = "/{workspace}/roles"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: read,
			Handler:     r.h.customRole.List,
			Summary:     "List custom roles",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: write,
			Handler:     okapi.H(r.h.customRole.Create),
			Summary:     "Create a custom role",
			Request:     &handlers.CustomRoleRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{id}",
			Group:       g,
			Middlewares: write,
			Handler:     okapi.H(r.h.customRole.Update),
			Summary:     "Update a custom role",
			Request:     &handlers.CustomRoleRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{id}",
			Group:       g,
			Middlewares: write,
			Handler:     r.h.customRole.Delete,
			Summary:     "Delete a custom role",
		},
		{
			Method:      http.MethodPut,
			Path:        "/{workspace}/members/{userID}/custom-role",
			Group:       g,
			Middlewares: write,
			Handler:     okapi.H(r.h.customRole.AssignMember),
			Summary:     "Assign a custom role to a member",
			Request:     &handlers.AssignCustomRoleRequest{},
		},

		// Workspace audit log export (admins; streamed JSON/CSV; gated audit_export).
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/audit/export",
			Group:       g,
			Middlewares: []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleAdmin)},
			Handler:     r.h.auditExport.WorkspaceExport,
			Summary:     "Export the workspace audit log (JSON/CSV)",
		},
	}
}
