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

func (r *Router) backupRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Backups", Description: "Database backups and schedules (pg-bkup/mysql-bkup/mongodb-bkup/libsql-bkup)."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/databases/{databaseID}/databases/{dbID}/backups"
	const sched = "/{workspace}/databases/{databaseID}/databases/{dbID}/backup-schedules"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.backup.List,
			Summary:     "List backups",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.backup.Run),
			Summary:     "Run a manual backup (local or s3)",
			Request:     &handlers.RunBackupRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{backupID}/restore",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.backup.Restore),
			Summary:     "Restore from a backup",
			Request:     &handlers.RestoreRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{workspace}/databases/{databaseID}/databases/{dbID}/restore-file",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.backup.RestoreFile,
			Summary:     "Restore from an uploaded dump (multipart: file, method)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{backupID}/download",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.backup.Download,
			Summary:     "Download a local backup artifact",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{backupID}/logs/download",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.backup.LogsDownload,
			Summary:     "Download a backup run's full logs",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{backupID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.backup.Delete,
			Summary:     "Delete a backup",
		},

		{
			Method:      http.MethodGet,
			Path:        sched,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.backup.ListSchedules,
			Summary:     "List backup schedules",
		},
		{
			Method:      http.MethodPost,
			Path:        sched,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.backup.CreateSchedule),
			Summary:     "Create a backup schedule",
			Request:     &handlers.CreateScheduleRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        sched + "/{scheduleID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.backup.DeleteSchedule,
			Summary:     "Delete a backup schedule",
		},
	}
}
