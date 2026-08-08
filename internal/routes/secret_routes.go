// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
)

// secretRoutes registers the workspace Vault. Viewer lists names only; Developer mutates;
// Admin reveals plaintext.
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
