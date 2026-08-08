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

func (r *Router) databaseRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Databases", Description: "Managed databases (PostgreSQL, MySQL, MariaDB, Redis, MongoDB, libSQL)."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/databases"

	return []okapi.RouteDefinition{
		// Resolved engine defaults (image/version from the deployment-config catalog).
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/database-engines",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.database.Engines,
			Summary:     "List engine default images/versions",
		},

		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.database.List,
			Summary:     "List database instances",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.database.Create),
			Summary:     "Provision a database instance",
			Request:     &handlers.CreateDatabaseRequest{},
		},
		// Static segment registered before "/{databaseID}" so it isn't captured as an id.
		{
			Method:      http.MethodGet,
			Path:        base + "/events",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.database.WorkspaceEvents,
			Summary:     "Stream live status of all instances in the workspace (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{databaseID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.database.Get,
			Summary:     "Get a database instance",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{databaseID}/status",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.database.Status,
			Summary:     "Get live instance status (one-shot)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{databaseID}/events",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.database.Events,
			Summary:     "Stream live instance status: provisioning/upgrade/start-stop (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{databaseID}/credentials",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.database.Credentials,
			Summary:     "Reveal the instance admin connection (admin)",
		},

		{
			Method:      http.MethodGet,
			Path:        base + "/{databaseID}/forward",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.database.ListForwards,
			Summary:     "List live forward sessions (admin)",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{databaseID}/forward",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.database.OpenForward,
			Summary:     "Open an external port-forward (admin)",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{databaseID}/forward/{sessionID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.database.CloseForward,
			Summary:     "Close a port-forward session (admin)",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{databaseID}/start",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.Start,
			Summary:     "Start the database instance",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{databaseID}/stop",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.Stop,
			Summary:     "Stop the database instance",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{databaseID}/restart",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.Restart,
			Summary:     "Restart the database instance",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{databaseID}/sync-sizes",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.SyncSizes,
			Summary:     "Refresh database size info",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{databaseID}/logs",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.database.Logs,
			Summary:     "Stream database container logs (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{databaseID}/upgrade",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.UpgradeOptions,
			Summary:     "Upgrade options + plan preview (use ?version= to preview a target)",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{databaseID}/upgrade",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.database.Upgrade),
			Summary:     "Upgrade the database engine version",
			Request:     &handlers.UpgradeDatabaseRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{databaseID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.Delete,
			Summary:     "Delete a database instance",
		},

		// Networks the instance is attached to (the default is always attached).
		{
			Method:      http.MethodPost,
			Path:        base + "/{databaseID}/networks/{networkID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.AttachNetwork,
			Summary:     "Attach a network to a database",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{databaseID}/networks/{networkID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.DetachNetwork,
			Summary:     "Detach a network from a database",
		},

		// Logical databases hosted on an instance (SQL engines).
		{
			Method:      http.MethodGet,
			Path:        base + "/{databaseID}/databases",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.database.ListDatabases,
			Summary:     "List databases on an instance",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{databaseID}/databases",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.database.CreateDatabase),
			Summary:     "Create a database (optionally attach to an app)",
			Request:     &handlers.CreateLogicalDatabaseRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{databaseID}/databases/{dbID}/connection",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.database.DatabaseConnection,
			Summary:     "Reveal a database connection (admin)",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{databaseID}/databases/{dbID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.DeleteDatabase,
			Summary:     "Delete a database",
		},
	}
}

func (r *Router) volumeRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Volumes", Description: "Persistent storage volumes."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/volumes"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.volume.List,
			Summary:     "List volumes",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/storage",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.volume.Storage,
			Summary:     "Workspace storage summary (declared vs measured usage)",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.volume.Create),
			Summary:     "Create a volume",
			Request:     &handlers.VolumeCreateRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{volumeID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.volume.Get,
			Summary:     "Get a volume (with usage)",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{volumeID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.volume.Delete,
			Summary:     "Delete a volume",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{volumeID}/files",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.volume.ListFiles,
			Summary:     "List files in a volume",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{volumeID}/files",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.volume.UploadFile,
			Summary:     "Upload a file into a volume (multipart: file, path)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{volumeID}/files/download",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.volume.DownloadFile,
			Summary:     "Download a file from a volume (query: path)",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{volumeID}/files",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.volume.DeleteFile,
			Summary:     "Delete a file from a volume (query: path)",
		},
	}
}
