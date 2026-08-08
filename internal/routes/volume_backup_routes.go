// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
)

func (r *Router) volumeBackupRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Volume Backups", Description: "Back up and restore volume data to S3 (volume-bkup)."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/volumes/{volumeID}/backups"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base + "/status",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.volumeBackup.Status,
			Summary:     "Whether volume backups are configured (S3)",
		},
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.volumeBackup.List,
			Summary:     "List volume backups",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.volumeBackup.Run,
			Summary:     "Back up a volume to S3",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{backupID}/restore",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.volumeBackup.Restore,
			Summary:     "Restore a volume from a backup",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{backupID}/logs/download",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.volumeBackup.LogsDownload,
			Summary:     "Download a volume-backup run's full logs",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{backupID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.volumeBackup.Delete,
			Summary:     "Delete a volume backup",
		},
	}
}
