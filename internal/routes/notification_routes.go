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

// notificationRoutes registers workspace notification channels: Viewer reads, Admin manages.
func (r *Router) notificationRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Notifications", Description: "Workspace notification channels (Telegram)."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/notifications/channels"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.notification.List,
			Summary:     "List notification channels",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.notification.Create),
			Summary:     "Create a notification channel",
			Request:     &handlers.CreateChannelRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{channelID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.notification.Get,
			Summary:     "Get a notification channel",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{channelID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.notification.Update),
			Summary:     "Update a notification channel",
			Request:     &handlers.UpdateChannelRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{channelID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.notification.Delete,
			Summary:     "Delete a notification channel",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{channelID}/test",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.notification.Test,
			Summary:     "Send a test notification",
		},
	}
}
