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

// webhookRoutes registers workspace webhooks. Viewer reads; Admin creates, updates, deletes and tests.
func (r *Router) webhookRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Webhooks", Description: "Outbound HTTP webhooks for application events."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/webhooks"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.webhook.List,
			Summary:     "List webhooks",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.webhook.Create),
			Summary:     "Create a webhook",
			Request:     &handlers.CreateWebhookRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{webhookID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.webhook.Get,
			Summary:     "Get a webhook",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{webhookID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.webhook.Update),
			Summary:     "Update a webhook",
			Request:     &handlers.UpdateWebhookRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{webhookID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.webhook.Delete,
			Summary:     "Delete a webhook",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{webhookID}/test",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.webhook.Test,
			Summary:     "Send a test delivery",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{webhookID}/deliveries",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.webhook.Deliveries,
			Summary:     "List recent deliveries",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{webhookID}/deliveries/{deliveryID}/redeliver",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.webhook.Redeliver,
			Summary:     "Re-send a past delivery",
		},
	}
}
