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

// locationRoutes registers where a workspace's resources can run: any member lists its locations,
// and workspace admins set the default one.
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
