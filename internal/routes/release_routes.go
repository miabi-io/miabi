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

func (r *Router) environmentRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Environments", Description: "Promotion stages (dev → staging → prod) with approval policy."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/environments"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.environment.List,
			Summary:     "List environments",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.environment.Create),
			Summary:     "Create an environment",
			Request:     &handlers.EnvironmentRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{environmentID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.environment.Get,
			Summary:     "Get an environment",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{environmentID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.environment.Update),
			Summary:     "Update an environment",
			Request:     &handlers.EnvironmentRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{environmentID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.environment.Delete,
			Summary:     "Delete an environment",
		},
	}
}

func (r *Router) releaseRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Releases", Description: "Promotable release artifacts with approval gates."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/releases"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.release.List,
			Summary:     "List releases",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{releaseID}/approvals",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.release.Approvals,
			Summary:     "Release approval status",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{releaseID}/approve",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.release.Approve),
			Summary:     "Approve a release for an environment",
			Request:     &handlers.ApproveReleaseRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{releaseID}/promote",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.release.Promote),
			Summary:     "Promote a release into an environment",
			Request:     &handlers.PromoteReleaseRequest{},
		},
	}
}
