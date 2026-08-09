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

// configRoutes registers workspace configuration files. Viewer lists keys and
// digests; Developer mutates; Admin reveals content, mirroring the Vault.
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
