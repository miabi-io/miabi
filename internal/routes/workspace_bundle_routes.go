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

// workspaceBundleRoutes registers portable workspace backup and restore. Admin-only throughout,
// including reads: a listing names every app, database and volume a workspace owns, and a
// restore creates resources, moves data and can add members — workspace administration.
func (r *Router) workspaceBundleRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{
		Name:        "Portable Backup",
		Description: "Export a workspace to an encrypted bundle on S3, and restore one back.",
	})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/portable-backup"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base + "/status",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Status,
			Summary:     "Whether portable backup is configured (S3 + passphrase)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/runs",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Runs,
			Summary:     "List export & restore runs",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/runs/{runID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Run,
			Summary:     "Get one run and its report",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/runs/{runID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.DeleteRun,
			Summary:     "Delete a run record (the bundle is kept)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/bundles",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Bundles,
			Summary:     "List the bundles in the bucket",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/bundles/{ref}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Bundle,
			Summary:     "Read one bundle's index",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/bundles/{ref}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.DeleteBundle,
			Summary:     "Delete a bundle from the bucket",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/export",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspaceBundle.Export,
			Summary:     "Export this workspace to a bundle",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/restore",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleOwner),
			Handler:     okapi.H(r.h.workspaceBundle.Restore),
			Summary:     "Restore a bundle into this or a new workspace",
			Request:     &handlers.RestoreBundleRequest{},
		},
	}
}
