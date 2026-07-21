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

// inboxRoutes registers the per-user notification inbox (the dashboard bell +
// Notifications page). User-scoped: authenticated only, no workspace scope — the
// handler filters to the caller's own items across their workspaces.
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
	}
}

// alertRoutes registers workspace-scoped alert views + lifecycle transitions (the
// shared conditions behind the per-user notifications).
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
