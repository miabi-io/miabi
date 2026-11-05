// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/wsbackup"
)

// migrationRoutes registers install-to-install migration. The source tree (/migration) answers only the
// key of a started migration and exists only when MIABI_MIGRATION_ENABLED is set; the rest is always
// registered and refuses with MIGRATION_DISABLED, so the console can explain why.
func (r *Router) migrationRoutes() []okapi.RouteDefinition {
	src := r.v1.Group("/migration").WithTagInfo(okapi.GroupTag{Name: "Migration (source)", Description: "What another Miabi pulls a workspace from, with a started migration's code. Off unless MIABI_MIGRATION_ENABLED."})
	if !r.cfg.MigrationEnabled {
		src.Disable()
	}
	key := []okapi.Middleware{r.authenticate, middlewares.RequireMigrationCaller()}

	ws := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Migration", Description: "Move a workspace to or from another Miabi."})
	owner := []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleOwner)}

	mig := r.v1.Group("/migrations").WithTagInfo(okapi.GroupTag{Name: "Migration"})
	user := []okapi.Middleware{r.authenticate}

	adm := r.v1.Group("/admin").WithTagInfo(okapi.GroupTag{Name: "Admin"})
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}

	return []okapi.RouteDefinition{
		{Method: http.MethodGet, Path: "/workspaces", Group: src, Middlewares: key, Handler: r.h.wsMigration.SourceInfo,
			Summary: "List the workspaces this migration key can export", Response: &dto.Response[wsbackup.MigrationSourceInfo]{}},
		{Method: http.MethodPost, Path: "/workspaces/{workspace}/exports", Group: src, Middlewares: key, Handler: okapi.H(r.h.wsMigration.StartExport),
			Summary: "Stage a workspace for the calling install to pull", Request: &handlers.MigrationExportRequest{}, Response: &dto.Response[wsbackup.MigrationExportView]{}},
		{Method: http.MethodGet, Path: "/workspaces/{workspace}/exports/{id}", Group: src, Middlewares: key, Handler: okapi.H(r.h.wsMigration.GetExport),
			Summary: "An export's progress and artifacts", Request: &handlers.MigrationExportRef{}, Response: &dto.Response[wsbackup.MigrationExportView]{}},
		{Method: http.MethodGet, Path: "/workspaces/{workspace}/exports/{id}/state", Group: src, Middlewares: key, Handler: okapi.H(r.h.wsMigration.ExportState),
			Summary: "The workspace's state document, secrets included", Request: &handlers.MigrationExportRef{}},
		{Method: http.MethodGet, Path: "/workspaces/{workspace}/exports/{id}/artifacts/{index}", Group: src, Middlewares: key, Handler: okapi.H(r.h.wsMigration.ExportArtifact),
			Summary: "One staged dump or archive; Range resumes", Request: &handlers.MigrationArtifactRef{}},
		{Method: http.MethodPost, Path: "/workspaces/{workspace}/exports/{id}/retire", Group: src, Middlewares: key, Handler: okapi.H(r.h.wsMigration.Retire),
			Summary: "Mark the workspace as moved; it stays frozen", Request: &handlers.MigrationRetireRequest{}, Response: &dto.Response[wsbackup.MigrationExportView]{}},
		{Method: http.MethodPost, Path: "/workspaces/{workspace}/exports/{id}/complete", Group: src, Middlewares: key, Handler: okapi.H(r.h.wsMigration.Complete),
			Summary: "Report the import complete; a workspace migration's code stops working", Request: &handlers.MigrationRetireRequest{}, Response: &dto.Response[wsbackup.MigrationExportView]{}},
		{Method: http.MethodPost, Path: "/workspaces/{workspace}/exports/{id}/unfreeze", Group: src, Middlewares: key, Handler: okapi.H(r.h.wsMigration.Unfreeze),
			Summary: "Roll the source back: unfreeze it", Request: &handlers.MigrationExportRef{}, Response: &dto.Response[wsbackup.MigrationExportView]{}},
		{Method: http.MethodDelete, Path: "/workspaces/{workspace}/exports/{id}", Group: src, Middlewares: key, Handler: okapi.H(r.h.wsMigration.DeleteExport),
			Summary: "Purge an export's staged data", Request: &handlers.MigrationExportRef{}},

		{Method: http.MethodGet, Path: "/{workspace}/migration", Group: ws, Middlewares: owner, Handler: r.h.wsMigration.WorkspaceMigration,
			Summary: "This workspace's migration: the open one, past pulls, and whether it is frozen", Response: &dto.Response[handlers.MigrationOverview]{}},
		{Method: http.MethodPost, Path: "/{workspace}/migration/start", Group: ws, Middlewares: owner, Handler: okapi.H(r.h.wsMigration.StartWorkspaceMigration),
			Summary: "Start moving this workspace to another Miabi", Description: "Owner only. Returns the migration code, shown once, that the other Miabi imports with. Single use: it stops working once an import completes, when cancelled, or when it expires (24 hours by default, 7 days at most). Starting again replaces it.",
			Request: &handlers.MigrationStartRequest{}, Response: &dto.Response[handlers.MigrationStarted]{}},
		{Method: http.MethodDelete, Path: "/{workspace}/migration", Group: ws, Middlewares: owner, Handler: r.h.wsMigration.CancelWorkspaceMigration,
			Summary: "Cancel this workspace's migration: its code stops working"},
		{Method: http.MethodPost, Path: "/{workspace}/migration/unfreeze", Group: ws, Middlewares: owner, Handler: r.h.wsMigration.UnfreezeWorkspace,
			Summary: "Unfreeze a workspace a migration froze", Description: "Rolls a migration back on the source: its apps start again and it accepts changes."},

		{Method: http.MethodGet, Path: "/status", Group: mig, Middlewares: user, Handler: r.h.wsMigration.Status,
			Summary: "Which migration directions this Miabi allows", Response: &dto.Response[handlers.MigrationStatus]{}},
		{Method: http.MethodPost, Path: "/probe", Group: mig, Middlewares: user, Handler: okapi.H(r.h.wsMigration.Probe),
			Summary: "Check a source Miabi and key, and list what it opens", Request: &handlers.MigrationSourceRequest{}, Response: &dto.Response[wsbackup.MigrationSourceInfo]{}},
		{Method: http.MethodPost, Path: "", Group: mig, Middlewares: user, Handler: okapi.H(r.h.wsMigration.Import),
			Summary: "Migrate a workspace from another Miabi into a new workspace here", Request: &handlers.MigrationImportRequest{}, Response: &dto.Response[models.WorkspaceBundle]{}},
		{Method: http.MethodGet, Path: "", Group: mig, Middlewares: user, Handler: r.h.wsMigration.ListImports,
			Summary: "Your migrations into this Miabi", Response: &dto.Response[[]models.WorkspaceBundle]{}},
		{Method: http.MethodGet, Path: "/{id}", Group: mig, Middlewares: user, Handler: okapi.H(r.h.wsMigration.GetImport),
			Summary: "One migration's progress and report", Request: &handlers.MigrationRunRef{}, Response: &dto.Response[models.WorkspaceBundle]{}},

		{Method: http.MethodGet, Path: "/migration", Group: adm, Middlewares: admin, Handler: r.h.wsMigration.InstanceMigration,
			Summary: "The install's migration: the open one and every pull", Response: &dto.Response[handlers.MigrationOverview]{}},
		{Method: http.MethodPost, Path: "/migration/start", Group: adm, Middlewares: admin, Handler: okapi.H(r.h.wsMigration.StartInstanceMigration),
			Summary: "Start moving workspaces of this install to another Miabi", Description: "Returns a code that opens every workspace here until cancelled or expired.",
			Request: &handlers.MigrationStartRequest{}, Response: &dto.Response[handlers.MigrationStarted]{}},
		{Method: http.MethodDelete, Path: "/migration", Group: adm, Middlewares: admin, Handler: r.h.wsMigration.CancelInstanceMigration,
			Summary: "Cancel the install's migration"},
		{Method: http.MethodGet, Path: "/workspace-migrations", Group: adm, Middlewares: admin, Handler: r.h.wsMigration.ListAllImports,
			Summary: "Every migration into this Miabi", Response: &dto.Response[[]models.WorkspaceBundle]{}},
		{Method: http.MethodPost, Path: "/workspace-migrations", Group: adm, Middlewares: admin, Handler: okapi.H(r.h.wsMigration.ImportBatch),
			Summary: "Migrate several workspaces from another Miabi", Request: &handlers.MigrationBatchRequest{}, Response: &dto.Response[handlers.MigrationBatch]{}},
	}
}
