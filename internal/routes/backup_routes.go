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
	// Sets are instance-level: they span every database on the instance, so they
	// hang off the instance rather than one of its databases.
	const sets = "/{workspace}/databases/{databaseID}/backup-sets"
	const setSched = "/{workspace}/databases/{databaseID}/backup-set-schedules"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        sets,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.backup.ListSets,
			Summary:     "List an instance's backup sets (recovery points)",
		},
		{
			Method:      http.MethodPost,
			Path:        sets,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.backup.RunSet),
			Summary:     "Back up every database on the instance as one recovery point",
			Request:     &handlers.RunBackupSetRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        sets + "/{setID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.backup.DeleteSet,
			Summary:     "Delete a backup set and its artifacts",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/backup-sets/discover",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.backup.DiscoverSets,
			Summary:     "List the recovery points in the workspace's bucket, including unknown ones",
		},
		{
			Method:      http.MethodPost,
			Path:        sets + "/adopt",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.backup.AdoptSet),
			Summary:     "Adopt a recovery point found in the bucket into this instance's history",
			Request:     &handlers.AdoptSetRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        sets + "/{setID}/restore",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.backup.RestoreSet),
			Summary:     "Restore every database in a recovery point",
			Request:     &handlers.RestoreSetRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        sets + "/{setID}/verify",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.backup.VerifySet,
			Summary:     "Re-check a recovery point against the bucket",
		},
		{
			Method:      http.MethodGet,
			Path:        sets + "/{setID}/recovery-kit",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.backup.RecoveryKit,
			Summary:     "Download instructions for restoring a recovery point without Miabi",
		},
		{
			Method:      http.MethodGet,
			Path:        setSched,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.backup.ListSetSchedules,
			Summary:     "List an instance's recovery-point schedules",
		},
		{
			Method:      http.MethodPost,
			Path:        setSched,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.backup.CreateSetSchedule),
			Summary:     "Schedule recovery points for the instance",
			Request:     &handlers.SetScheduleRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        setSched + "/{scheduleID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.backup.DeleteSetSchedule,
			Summary:     "Delete a recovery-point schedule",
		},
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
			Method:      http.MethodPatch,
			Path:        base + "/{backupID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.backup.Update),
			Summary:     "Edit a backup's comment and retention pin",
			Request:     &handlers.UpdateBackupRequest{},
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
