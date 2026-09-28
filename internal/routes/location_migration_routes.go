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

// locationMigrationRoutes registers live location migration. Workspace admins only, reads included: a
// migration moves and eventually deletes data, and its plan lists every volume and database an app uses.
func (r *Router) locationMigrationRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{
		Name:        "Location Migration",
		Description: "Move an application, its volumes and its databases to another location while it keeps serving (Enterprise).",
	})
	admin := []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleAdmin)}
	const app = "/{workspace}/apps/{appID}/migrations"
	const one = "/{workspace}/migrations/{migrationID}"
	platform := r.v1.Group("/admin").WithTagInfo(okapi.GroupTag{Name: "Admin"})

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodPost,
			Path:        app + "/plan",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.migration.Plan),
			Summary:     "Plan moving an application to another location (dry run)",
			Request:     &handlers.MigrationPlanRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        app,
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.migration.Start),
			Summary:     "Start moving an application to another location",
			Request:     &handlers.StartMigrationRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        app,
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.migration.ListForApp,
			Summary:     "List an application's migrations",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/migrations",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.migration.List,
			Summary:     "List the workspace's migrations",
		},
		{
			Method:      http.MethodGet,
			Path:        one,
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.migration.Get,
			Summary:     "Get a migration with its progress and report",
		},
		{
			Method:      http.MethodGet,
			Path:        one + "/events",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.migration.Events,
			Summary:     "Stream a migration's progress (SSE)",
		},
		{
			Method:      http.MethodPost,
			Path:        one + "/cutover",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.migration.Cutover,
			Summary:     "Cut over a migration waiting for it",
		},
		{
			Method:      http.MethodPost,
			Path:        one + "/cancel",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.migration.Cancel,
			Summary:     "Cancel a migration before the application goes down",
		},
		{
			Method:      http.MethodPost,
			Path:        one + "/rollback",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.migration.Rollback,
			Summary:     "Roll a migration back to the source location",
		},
		{
			Method:      http.MethodPost,
			Path:        one + "/finalize",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.migration.Finalize,
			Summary:     "Delete the source copy now, ending the rollback window",
		},
		{
			Method:      http.MethodGet,
			Path:        "/migrations",
			Group:       platform,
			Middlewares: []okapi.Middleware{r.authenticate, r.systemAdmin},
			Handler:     r.h.migration.AdminList,
			Summary:     "List every location migration on the platform",
		},
	}
}
