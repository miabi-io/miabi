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

// Viewer lists names only; Developer mutates; Admin reveals plaintext.
func (r *Router) secretRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Secrets", Description: "Workspace secrets (Vault), referenced from env as ${{ secrets.NAME }}."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/secrets"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.secret.List,
			Summary:     "List secrets (no values)",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.secret.Create),
			Summary:     "Create a secret",
			Request:     &handlers.CreateSecretRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{secretID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.secret.Update),
			Summary:     "Update a secret (rotate value)",
			Request:     &handlers.UpdateSecretRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{secretID}/reveal",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.secret.Reveal,
			Summary:     "Reveal a secret's value (admin)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{secretID}/usage",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.secret.Usage,
			Summary:     "List apps referencing a secret",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{secretID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.secret.Delete,
			Summary:     "Delete a secret",
		},
	}
}
