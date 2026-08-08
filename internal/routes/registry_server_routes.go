// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
)

// registryServerRoutes registers the built-in registry's admin settings, per-workspace info,
// and the internal gateway forwardAuth endpoint.
func (r *Router) registryServerRoutes() []okapi.RouteDefinition {
	admin := r.v1.Group("/admin").WithTagInfo(okapi.GroupTag{Name: "Admin", Description: "Platform administration."})
	ws := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Workspaces", Description: "Workspaces."})
	adminMw := []okapi.Middleware{r.authenticate, r.systemAdmin}
	scoped := []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleViewer)}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/registry/settings",
			Group:       admin,
			Middlewares: adminMw,
			Handler:     r.h.adminRegistry.GetSettings,
			Summary:     "Get internal registry settings",
		},
		{
			Method:      http.MethodPut,
			Path:        "/registry/settings",
			Group:       admin,
			Middlewares: adminMw,
			Handler:     okapi.H(r.h.adminRegistry.UpdateSettings),
			Summary:     "Update internal registry settings",
			Request:     &handlers.UpdateRegistrySettingsRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/registry/runtime",
			Group:       admin,
			Middlewares: adminMw,
			Handler:     r.h.adminRegistry.Runtime,
			Summary:     "Get the registry container's live state and resource usage",
		},
		{
			Method:      http.MethodPost,
			Path:        "/registry/gc",
			Group:       admin,
			Middlewares: adminMw,
			Handler:     r.h.adminRegistry.RunGC,
			Summary:     "Run registry garbage collection",
			Response:    &dto.Response[dto.MessageData]{},
		},

		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/registry",
			Group:       ws,
			Middlewares: scoped,
			Handler:     r.h.registryServer.Info,
			Summary:     "Workspace registry connection info",
			Response:    &dto.Response[handlers.RegistryInfo]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/registry/repositories",
			Group:       ws,
			Middlewares: scoped,
			Handler:     r.h.registryServer.Repositories,
			Summary:     "List workspace registry repositories (paged)",
		},
		{
			// ?name= rather than a path segment: an image name may contain slashes.
			Method:      http.MethodGet,
			Path:        "/{workspace}/registry/repository",
			Group:       ws,
			Middlewares: scoped,
			Handler:     r.h.registryServer.RepositoryOverview,
			Summary:     "Get a registry repository's overview",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/registry/repository/tags",
			Group:       ws,
			Middlewares: scoped,
			Handler:     r.h.registryServer.RepositoryTags,
			Summary:     "List a registry repository's tags (paged)",
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{workspace}/registry/repositories/{repo}/tags/{tag}",
			Group:       ws,
			Middlewares: []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleDeveloper)},
			Handler:     r.h.registryServer.DeleteTag,
			Summary:     "Delete a registry tag",
			Response:    &dto.Response[dto.MessageData]{},
		},

		// Internal: Goma forwardAuth target (unauthenticated; gateway-only). Hit on
		// every /v2/* request to allow/deny by docker Basic credentials.
		{
			Method:  http.MethodPost,
			Path:    "/internal/registry/auth",
			Handler: r.h.registryServer.Authenticate,
			Tags:    []string{"Internal"},
			Summary: "Registry forwardAuth (gateway-only)",
		},
		{
			// GET variant: docker login's initial /v2/ probe forwards a GET; some
			// forwardAuth setups replay the original method, so accept both.
			Method:  http.MethodGet,
			Path:    "/internal/registry/auth",
			Handler: r.h.registryServer.Authenticate,
			Tags:    []string{"Internal"},
			Summary: "Registry forwardAuth (gateway-only)",
		},
	}
}
