// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
)

// workspaceKeysRoutes registers a workspace's encryption and sealing keys. Rotation is owner/admin only and
// session only: an API key must not be able to rotate the keys that protect the workspace.
func (r *Router) workspaceKeysRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Workspace Keys", Description: "Per-workspace encryption and sealing keys."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	manage := []okapi.Middleware{r.authenticate, middlewares.RequireSession(), r.scope, middlewares.RequireRole(models.WorkspaceRoleAdmin)}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/sealing-key",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.workspaceKeys.SealingKey,
			Summary:     "Get the public key that secrets are sealed to",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/encryption",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceKeys.Status,
			Summary:     "Get the workspace's key versions, rotation dates and sealing keys",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{workspace}/encryption/rotate",
			Group:       g,
			Middlewares: manage,
			Handler:     r.h.workspaceKeys.Rotate,
			Summary:     "Rotate the workspace's data and sealing keys (at most once every 6 months)",
		},
	}
}
