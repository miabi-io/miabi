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

// No workspace scope: the handler filters to the caller's own items across their workspaces.
func (r *Router) inboxRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/notifications").WithTagInfo(okapi.GroupTag{Name: "Inbox", Description: "Per-user notification inbox (bell)."})
	auth := []okapi.Middleware{r.authenticate}

	return []okapi.RouteDefinition{
		{Method: http.MethodGet, Path: "", Group: g, Middlewares: auth, Handler: r.h.inbox.List,
			Summary: "List my notifications (?workspace=&unread=&before=&limit=)"},
		{Method: http.MethodGet, Path: "/unread-count", Group: g, Middlewares: auth, Handler: r.h.inbox.UnreadCount,
			Summary: "My unread notification count (bell badge)"},
		{Method: http.MethodGet, Path: "/stream", Group: g, Middlewares: auth, Handler: r.h.inbox.Stream,
			Summary: "Live inbox updates (SSE)"},
		{Method: http.MethodPost, Path: "/read", Group: g, Middlewares: auth, Handler: okapi.H(r.h.inbox.MarkRead),
			Request: &handlers.MarkReadRequest{}, Summary: "Mark notifications read"},
		{Method: http.MethodPost, Path: "/read-all", Group: g, Middlewares: auth, Handler: r.h.inbox.MarkAllRead,
			Summary: "Mark all my notifications read (?workspace=)"},
		{Method: http.MethodGet, Path: "/banners", Group: g, Middlewares: auth, Handler: r.h.inbox.Banners,
			Summary: "My pinned, undismissed notices (app-wide banner)"},
		{Method: http.MethodPost, Path: "/dismiss", Group: g, Middlewares: auth, Handler: okapi.H(r.h.inbox.Dismiss),
			Request: &handlers.DismissRequest{}, Summary: "Dismiss notices from the banner"},
	}
}

// alertRoutes registers workspace-scoped alert views and lifecycle transitions.
func (r *Router) alertRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Alerts", Description: "Workspace alerts (deduplicated conditions)."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/alerts"

	return []okapi.RouteDefinition{
		{Method: http.MethodGet, Path: base, Group: g, Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler: r.h.alerts.List, Summary: "List workspace alerts (?active=true)"},
		{Method: http.MethodPost, Path: base + "/{alertID}/ack", Group: g, Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler: r.h.alerts.Acknowledge, Summary: "Acknowledge an alert"},
		{Method: http.MethodPost, Path: base + "/{alertID}/resolve", Group: g, Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler: r.h.alerts.Resolve, Summary: "Resolve an alert"},
	}
}
