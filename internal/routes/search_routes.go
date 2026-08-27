// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
)

func (r *Router) searchRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{
		Name:        "Search",
		Description: "Cross-resource search within a workspace.",
	})
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/search",
			Group:       g,
			Middlewares: []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleViewer)},
			Handler:     r.h.search.Search,
			Summary:     "Search applications, databases, routes and other resources in a workspace",
		},
	}
}
