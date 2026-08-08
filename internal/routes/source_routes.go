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

func (r *Router) registryRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Registries", Description: "Container registry credentials for pulling private images."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/registries"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.registry.List,
			Summary:     "List registries",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.registry.Create),
			Summary:     "Add a registry credential",
			Request:     &handlers.CreateRegistryRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{registryID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.registry.Get,
			Summary:     "Get a registry",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{registryID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.registry.Update),
			Summary:     "Update a registry credential",
			Request:     &handlers.UpdateRegistryRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{registryID}/test",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.registry.Test,
			Summary:     "Test registry authentication",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{registryID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.registry.Delete,
			Summary:     "Delete a registry credential",
		},
	}
}

func (r *Router) gitRepositoryRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Git Repositories", Description: "Git credentials for cloning private repositories."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/git-repositories"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.gitRepo.List,
			Summary:     "List git repositories",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.gitRepo.Create),
			Summary:     "Add a git repository credential",
			Request:     &handlers.CreateGitRepoRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{gitRepoID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.gitRepo.Get,
			Summary:     "Get a git repository",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{gitRepoID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.gitRepo.Update),
			Summary:     "Update a git repository credential",
			Request:     &handlers.UpdateGitRepoRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{gitRepoID}/test",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.gitRepo.Test,
			Summary:     "Test git connection",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{gitRepoID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.gitRepo.Delete,
			Summary:     "Delete a git repository credential",
		},
		{
			// Not credential CRUD, but the same domain: what a repository holds,
			// asked before an app exists to hang the question off.
			Method:      http.MethodPost,
			Path:        "/{workspace}/git/inspect",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.gitInspect.Inspect),
			Summary:     "Inspect a repository for a Dockerfile and pipeline-as-code",
			Request:     &handlers.InspectGitRequest{},
		},
	}
}
