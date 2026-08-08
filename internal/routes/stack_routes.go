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

// stackRoutes registers workspace-scoped application stacks. Viewer reads; Developer creates,
// edits and assigns apps; Admin deletes.
func (r *Router) stackRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Stacks", Description: "Group applications into stacks."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/stacks"
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.stack.List,
			Summary:     "List stacks",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.stack.Create),
			Summary:     "Create a stack",
			Request:     &handlers.CreateStackRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/import",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.stack.Import),
			Summary:     "Import a stack from docker-compose",
			Request:     &handlers.ImportStackRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{stackID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.stack.Get,
			Summary:     "Get a stack with its apps",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{stackID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.stack.Update),
			Summary:     "Update a stack",
			Request:     &handlers.UpdateStackRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{stackID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.stack.Delete,
			Summary:     "Delete a stack (detaches its apps)",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{stackID}/apps/{appID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.stack.AddApp,
			Summary:     "Add an application to a stack",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{stackID}/apps/{appID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.stack.RemoveApp,
			Summary:     "Remove an application from a stack",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{stackID}/start",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.stack.Start,
			Summary:     "Start all applications in a stack",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{stackID}/stop",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.stack.Stop,
			Summary:     "Stop all applications in a stack",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{stackID}/restart",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.stack.Restart,
			Summary:     "Restart all applications in a stack (?rolling=true)",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{stackID}/deploy",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.stack.DeployAll,
			Summary:     "Deploy all applications in a stack",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{stackID}/events",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.stack.Events,
			Summary:     "List a stack's combined activity feed",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{stackID}/env",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.stack.ListEnvVars,
			Summary:     "List a stack's shared env vars",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{stackID}/env",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.stack.SetEnvVar),
			Summary:     "Set a shared env var",
			Request:     &handlers.SetStackEnvVarRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{stackID}/env/import",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.stack.ImportEnvVars),
			Summary:     "Bulk-import shared env vars from .env",
			Request:     &handlers.ImportEnvVarsRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{stackID}/env/{key}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.stack.DeleteEnvVar,
			Summary:     "Delete a shared env var",
		},
	}
}
