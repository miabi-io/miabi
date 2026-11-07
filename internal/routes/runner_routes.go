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

// Mutations need workspace admin since they register build infrastructure.
func (r *Router) runnerRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Runners", Description: "Dedicated build & pipeline execution machines."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/runners"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.runner.List,
			Summary:     "List the workspace's runners",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.runner.Create),
			Summary:     "Register a runner (returns a one-time registration token)",
			Request:     &handlers.RunnerRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/events",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.runner.StatusEvents,
			Summary:     "Stream the workspace's runners connecting and disconnecting (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/shared",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.runner.ListShared,
			Summary:     "List the platform-shared runners (read-only)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{runnerID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.runner.Get,
			Summary:     "Get a runner",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{runnerID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.runner.Update),
			Summary:     "Update a runner (labels, concurrency, name)",
			Request:     &handlers.RunnerRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{runnerID}/cordon",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.runner.Cordon),
			Summary:     "Cordon or uncordon a runner (hold out of scheduling)",
			Request:     &handlers.RunnerCordonRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{runnerID}/token",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.runner.RegenerateToken,
			Summary:     "Regenerate a runner's registration token",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{runnerID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.runner.Delete,
			Summary:     "Delete a runner",
		},
	}
}

// Token-authenticated outbound WebSocket, registered on the app to bypass v1 auth; uses the agent limiter.
func (r *Router) runnerGatewayRoutes() []okapi.RouteDefinition {
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/api/v1/runner/connect",
			Middlewares: []okapi.Middleware{r.agentRateLimit},
			Handler:     r.h.runnerGateway.Connect,
			Tags:        []string{"Runners"},
			Summary:     "Runner tunnel (WebSocket; token-authenticated)",
		},
	}
}

func (r *Router) adminRunnerRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/admin/runners").WithTagInfo(okapi.GroupTag{Name: "Runners", Description: "Platform-shared build & pipeline runners."})
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminRunner.List,
			Summary:     "List platform-shared runners",
		},
		{
			Method:      http.MethodPost,
			Path:        "",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminRunner.Create),
			Summary:     "Register a shared runner (returns a one-time token)",
			Request:     &handlers.RunnerRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/events",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminRunner.StatusEvents,
			Summary:     "Stream the shared runners connecting and disconnecting (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{runnerID}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminRunner.Get,
			Summary:     "Get a shared runner",
		},
		{
			Method:      http.MethodPut,
			Path:        "/{runnerID}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminRunner.Update),
			Summary:     "Update a shared runner",
			Request:     &handlers.RunnerRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{runnerID}/cordon",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminRunner.Cordon),
			Summary:     "Cordon or uncordon a shared runner",
			Request:     &handlers.RunnerCordonRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{runnerID}/token",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminRunner.RegenerateToken,
			Summary:     "Regenerate a shared runner's registration token",
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{runnerID}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminRunner.Delete,
			Summary:     "Delete a shared runner",
		},
	}
}
