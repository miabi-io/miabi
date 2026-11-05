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

package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/services/wsbackup"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// Migration key lifetimes: every key expires, and none outlives a week.
const (
	defaultMigrationKeyTTL = 24 * time.Hour
	maxMigrationKeyTTL     = 7 * 24 * time.Hour
)

// migrationError is a refusal with a stable code in the response envelope.
type migrationError struct{ code, msg string }

func (e migrationError) Error() string { return e.msg }
func (e migrationError) Code() string  { return e.code }

var (
	errMigrationOff      = migrationError{"MIGRATION_DISABLED", "migration is not enabled on this Miabi: set MIABI_MIGRATION_ENABLED=true"}
	errMigrationKeyScope = migrationError{"MIGRATION_KEY_SCOPE", "this migration code does not open that workspace"}
)

// MigrationHandler serves both ends of an install-to-install migration.
type MigrationHandler struct {
	svc        *wsbackup.Service
	keys       *auth.APIKeyService
	keyRepo    *repositories.APIKeyRepository
	workspaces *repositories.WorkspaceRepository
	users      *repositories.UserRepository
	audit      *audit.Logger
	enabled    bool
	publicURL  string
	installID  string
	version    string
}

// MigrationHandlerDeps are the handler's collaborators.
type MigrationHandlerDeps struct {
	Svc        *wsbackup.Service
	Keys       *auth.APIKeyService
	KeyRepo    *repositories.APIKeyRepository
	Workspaces *repositories.WorkspaceRepository
	Users      *repositories.UserRepository
	Audit      *audit.Logger
	// Enabled is MIABI_MIGRATION_ENABLED.
	Enabled   bool
	PublicURL string
	InstallID string
	Version   string
}

func NewMigrationHandler(d MigrationHandlerDeps) *MigrationHandler {
	return &MigrationHandler{svc: d.Svc, keys: d.Keys, keyRepo: d.KeyRepo, workspaces: d.Workspaces, users: d.Users,
		audit: d.Audit, enabled: d.Enabled, publicURL: d.PublicURL, installID: d.InstallID, version: d.Version}
}

// MigrationStatus tells the UI whether this install takes part in migrations.
type MigrationStatus struct {
	Enabled bool `json:"enabled"`
	// URL is this install's address, the one a migration code carries.
	URL string `json:"url,omitempty"`
}

// Status reports MIABI_MIGRATION_ENABLED, for any signed-in user.
func (h *MigrationHandler) Status(c *okapi.Context) error {
	return ok(c, MigrationStatus{Enabled: h.enabled, URL: h.publicURL})
}

func (h *MigrationHandler) keyUser(c *okapi.Context) (*models.User, error) {
	return h.users.FindByID(uint(middlewares.UserID(c)))
}

// sourceWorkspace resolves the named workspace and checks the key opens it: a workspace key its own,
// held by that workspace's owner; an instance key any but the system workspace, held by a platform admin.
func (h *MigrationHandler) sourceWorkspace(c *okapi.Context, name string) (*models.Workspace, error) {
	ws, err := h.workspaces.FindByName(name)
	if err != nil || ws.System {
		return nil, errMigrationKeyScope
	}
	if !h.keyOpens(c, ws) {
		return nil, errMigrationKeyScope
	}
	return ws, nil
}

func (h *MigrationHandler) keyOpens(c *okapi.Context, ws *models.Workspace) bool {
	user, err := h.keyUser(c)
	if err != nil {
		return false
	}
	if bound := middlewares.APIKeyWorkspaceID(c); bound != nil {
		if *bound != ws.ID {
			return false
		}
		m, err := h.workspaces.FindMember(ws.ID, user.ID)
		return err == nil && m.Role == models.WorkspaceRoleOwner
	}
	return user.IsAdmin()
}

// SourceInfo lists what the presented key may export.
func (h *MigrationHandler) SourceInfo(c *okapi.Context) error {
	info := wsbackup.MigrationSourceInfo{InstallID: h.installID, Version: h.version, Schema: wsbundle.StateSchema, Workspaces: []wsbackup.MigrationWorkspace{}}
	var candidates []models.Workspace
	if bound := middlewares.APIKeyWorkspaceID(c); bound != nil {
		if ws, err := h.workspaces.FindByID(*bound); err == nil {
			candidates = append(candidates, *ws)
		}
	} else {
		all, err := h.workspaces.ListAll()
		if err != nil {
			return c.AbortInternalServerError("failed to list workspaces", err)
		}
		candidates = all
	}
	for i := range candidates {
		ws := &candidates[i]
		if ws.System || !h.keyOpens(c, ws) {
			continue
		}
		apps, dbs, vols, size := h.svc.MigrationPreview(ws.ID)
		info.Workspaces = append(info.Workspaces, wsbackup.MigrationWorkspace{
			Name: ws.Name, DisplayName: ws.DisplayName, Frozen: ws.Restriction == models.WorkspaceRestrictionFrozen,
			Apps: apps, Databases: dbs, Volumes: vols, SizeBytes: size,
		})
	}
	return ok(c, info)
}

type MigrationExportRequest struct {
	Workspace string `path:"workspace" required:"true"`
	Body      struct {
		Freeze bool `json:"freeze" doc:"Stop the workspace's apps and refuse changes before the copy"`
		Data   bool `json:"data" doc:"Dump the databases and archive the volumes too"`
	} `json:"body"`
}

// StartExport stages the workspace for the caller to pull.
func (h *MigrationHandler) StartExport(c *okapi.Context, req *MigrationExportRequest) error {
	ws, err := h.sourceWorkspace(c, req.Workspace)
	if err != nil {
		return c.AbortWithError(403, err)
	}
	run, err := h.svc.StartMigrationExport(c.Request().Context(), ws.ID, uint(middlewares.APIKeyID(c)), uint(middlewares.UserID(c)),
		wsbackup.MigrationExportInput{Freeze: req.Body.Freeze, Data: req.Body.Data})
	if err != nil {
		return moveAbort(c, err, "failed to start the export")
	}
	h.record(c, ws.ID, "workspace.migration_export", run.Ref, map[string]any{"freeze": req.Body.Freeze, "data": req.Body.Data, "ip": c.RealIP()})
	return ok(c, wsbackup.ExportView(run))
}

type MigrationExportRef struct {
	Workspace string `path:"workspace" required:"true"`
	ID        uint   `path:"id" required:"true"`
}

func (h *MigrationHandler) loadExport(c *okapi.Context, ref *MigrationExportRef) (*models.WorkspaceBundle, error) {
	ws, err := h.sourceWorkspace(c, ref.Workspace)
	if err != nil {
		return nil, err
	}
	return h.svc.MigrationExport(ws.ID, ref.ID)
}

// GetExport reports an export's progress and, once ready, its artifacts.
func (h *MigrationHandler) GetExport(c *okapi.Context, req *MigrationExportRef) error {
	b, err := h.loadExport(c, req)
	if err != nil {
		return moveAbort(c, err, "failed to load the export")
	}
	return ok(c, wsbackup.ExportView(b))
}

// ExportState serves the workspace's state document. It carries every secret value, so the response is
// never cached and its pull is audited.
func (h *MigrationHandler) ExportState(c *okapi.Context, req *MigrationExportRef) error {
	b, err := h.loadExport(c, req)
	if err != nil {
		return moveAbort(c, err, "failed to load the export")
	}
	body, err := h.svc.MigrationState(b)
	if err != nil {
		return moveAbort(c, err, "failed to read the export")
	}
	h.record(c, b.WorkspaceID, "workspace.migration_state_read", b.Ref, map[string]any{"ip": c.RealIP()})
	c.SetHeader("Content-Type", "application/json")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("Content-Length", strconv.Itoa(len(body)))
	w := c.Response()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}

type MigrationArtifactRef struct {
	Workspace string `path:"workspace" required:"true"`
	ID        uint   `path:"id" required:"true"`
	// Not marked required: the binder reads 0 as absent, and 0 is the first artifact.
	Index int `path:"index"`
}

// ExportArtifact streams one staged artifact. A "Range: bytes=N-" request resumes from byte N.
func (h *MigrationHandler) ExportArtifact(c *okapi.Context, req *MigrationArtifactRef) error {
	b, err := h.loadExport(c, &MigrationExportRef{Workspace: req.Workspace, ID: req.ID})
	if err != nil {
		return moveAbort(c, err, "failed to load the export")
	}
	rc, size, art, err := h.svc.OpenMigrationArtifact(c.Request().Context(), b, req.Index)
	if err != nil {
		return moveAbort(c, err, "failed to read the artifact")
	}
	defer func() { _ = rc.Close() }()
	offset, err := rangeStart(c.Header("Range"), size)
	if err != nil {
		c.SetHeader("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
		return c.AbortWithError(http.StatusRequestedRangeNotSatisfiable, err)
	}
	status := http.StatusOK
	if offset > 0 {
		// The file is read from a container's tar stream, which cannot seek: skip to the offset.
		if _, err := io.CopyN(io.Discard, rc, offset); err != nil {
			return c.AbortInternalServerError("failed to read the artifact", err)
		}
		status = http.StatusPartialContent
		c.SetHeader("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, size-1, size))
	}
	c.SetHeader("Content-Type", "application/octet-stream")
	c.SetHeader("Accept-Ranges", "bytes")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Miabi-SHA256", art.SHA256)
	if size > 0 {
		c.SetHeader("Content-Length", strconv.FormatInt(size-offset, 10))
	}
	w := c.Response()
	w.WriteHeader(status)
	_, _ = io.Copy(w, rc)
	return nil
}

// rangeStart reads the start of a "bytes=N-" range; anything else is served whole.
func rangeStart(h string, size int64) (int64, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, nil
	}
	spec, found := strings.CutPrefix(h, "bytes=")
	start, rest, dash := strings.Cut(spec, "-")
	if !found || !dash || strings.Contains(rest, ",") {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	if err != nil || n < 0 {
		return 0, nil
	}
	if size > 0 && n >= size {
		return 0, errors.New("the range starts past the end of the artifact")
	}
	return n, nil
}

type MigrationRetireRequest struct {
	Workspace string `path:"workspace" required:"true"`
	ID        uint   `path:"id" required:"true"`
	Body      struct {
		Target string `json:"target" doc:"Where the workspace now lives, shown to anyone who opens it here"`
	} `json:"body"`
}

// Retire records that the workspace moved: it stays frozen, saying where it went.
func (h *MigrationHandler) Retire(c *okapi.Context, req *MigrationRetireRequest) error {
	b, err := h.loadExport(c, &MigrationExportRef{Workspace: req.Workspace, ID: req.ID})
	if err != nil {
		return moveAbort(c, err, "failed to load the export")
	}
	if err := h.svc.RetireMigrationExport(b, req.Body.Target); err != nil {
		return moveAbort(c, err, "failed to retire the workspace")
	}
	h.record(c, b.WorkspaceID, "workspace.migration_retire", b.Ref, map[string]any{"target": req.Body.Target})
	return ok(c, wsbackup.ExportView(b))
}

// Complete records that the importer has the workspace. A workspace migration is single use, so its
// code stops working; an instance migration stays open for the importer's other workspaces.
func (h *MigrationHandler) Complete(c *okapi.Context, req *MigrationRetireRequest) error {
	b, err := h.loadExport(c, &MigrationExportRef{Workspace: req.Workspace, ID: req.ID})
	if err != nil {
		return moveAbort(c, err, "failed to load the export")
	}
	if err := h.svc.CompleteMigrationExport(b, req.Body.Target); err != nil {
		return moveAbort(c, err, "failed to record the import")
	}
	if middlewares.APIKeyWorkspaceID(c) != nil {
		if err := h.keys.Revoke(uint(middlewares.APIKeyID(c))); err != nil {
			return c.AbortInternalServerError("failed to close the migration", err)
		}
	}
	h.record(c, b.WorkspaceID, "workspace.migration_complete", b.Ref, map[string]any{"target": req.Body.Target})
	return ok(c, wsbackup.ExportView(b))
}

// Unfreeze rolls the source back: its apps start again and it accepts changes.
func (h *MigrationHandler) Unfreeze(c *okapi.Context, req *MigrationExportRef) error {
	b, err := h.loadExport(c, req)
	if err != nil {
		return moveAbort(c, err, "failed to load the export")
	}
	if err := h.svc.UnfreezeMigration(c.Request().Context(), b.WorkspaceID); err != nil {
		return moveAbort(c, err, "failed to unfreeze the workspace")
	}
	h.record(c, b.WorkspaceID, "workspace.migration_unfreeze", b.Ref, nil)
	return ok(c, wsbackup.ExportView(b))
}

// DeleteExport purges an export's staged data once the importer has it.
func (h *MigrationHandler) DeleteExport(c *okapi.Context, req *MigrationExportRef) error {
	b, err := h.loadExport(c, req)
	if err != nil {
		return moveAbort(c, err, "failed to load the export")
	}
	if err := h.svc.PurgeMigrationExport(c.Request().Context(), b); err != nil {
		return moveAbort(c, err, "failed to purge the export")
	}
	return ok(c, map[string]any{"purged": true})
}

// MigrationStartRequest opens a migration for another install to pull from.
type MigrationStartRequest struct {
	Body struct {
		ExpiresInHours int `json:"expires_in_hours" doc:"1 to 168; 24 when omitted"`
	} `json:"body"`
}

// MigrationStarted is shown once: the code cannot be read again, only replaced by starting anew.
type MigrationStarted struct {
	Code      string    `json:"code"`
	SourceURL string    `json:"source_url"`
	Workspace string    `json:"workspace,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

// MigrationWindow is an open migration: while it lasts, its code lets one install pull.
type MigrationWindow struct {
	StartedAt  time.Time  `json:"started_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// MigrationExportSummary is one pull of this workspace (or install) by another install.
type MigrationExportSummary struct {
	ID         uint                `json:"id"`
	Workspace  string              `json:"workspace"`
	Status     models.BackupStatus `json:"status"`
	Phase      string              `json:"phase,omitempty"`
	Error      string              `json:"error,omitempty"`
	Freeze     bool                `json:"freeze"`
	Data       bool                `json:"data"`
	Completed  bool                `json:"completed"`
	ImportedBy string              `json:"imported_by,omitempty"`
	CreatedAt  time.Time           `json:"created_at"`
}

// MigrationOverview is the Migration page of a workspace, or of the install for a platform admin.
type MigrationOverview struct {
	Enabled bool `json:"enabled"`
	// Window is the open migration, nil when there is none.
	Window       *MigrationWindow         `json:"window"`
	Frozen       bool                     `json:"frozen"`
	FrozenReason string                   `json:"frozen_reason,omitempty"`
	Exports      []MigrationExportSummary `json:"exports"`
	URL          string                   `json:"url,omitempty"`
}

// activeWindows are the migrations open for a workspace, or the install's when wsID is nil.
func (h *MigrationHandler) activeWindows(wsID *uint) ([]models.APIKey, error) {
	keys, err := h.keyRepo.ListByScope(models.ScopeMigration, wsID)
	if err != nil {
		return nil, err
	}
	out := keys[:0]
	for _, k := range keys {
		if k.IsValid() {
			out = append(out, k)
		}
	}
	return out, nil
}

func (h *MigrationHandler) closeWindows(wsID *uint) error {
	open, err := h.activeWindows(wsID)
	if err != nil {
		return err
	}
	for _, k := range open {
		if err := h.keys.Revoke(k.ID); err != nil {
			return err
		}
	}
	return nil
}

// startFor opens a migration held by userID — the owner, a platform admin, or an account's owner when
// the Billing API asks — replacing any open one, and returns its code.
func (h *MigrationHandler) startFor(userID uint, wsID *uint, hours int) (*MigrationStarted, error) {
	if !h.enabled {
		return nil, errMigrationOff
	}
	ttl := defaultMigrationKeyTTL
	if hours != 0 {
		ttl = time.Duration(hours) * time.Hour
	}
	if ttl < time.Hour || ttl > maxMigrationKeyTTL {
		return nil, migrationError{"INVALID_EXPIRY", "a migration lasts 1 to 168 hours"}
	}
	if err := h.closeWindows(wsID); err != nil {
		return nil, err
	}
	out := &MigrationStarted{SourceURL: h.publicURL, ExpiresAt: time.Now().Add(ttl).UTC()}
	name := "Instance migration"
	if wsID != nil {
		ws, err := h.workspaces.FindByID(*wsID)
		if err != nil {
			return nil, err
		}
		out.Workspace, name = ws.Name, "Migration of "+ws.Name
	}
	plain, _, err := h.keys.CreateMigration(userID, wsID, name, out.ExpiresAt)
	if err != nil {
		return nil, err
	}
	out.Code = wsbackup.EncodeMigrationCode(h.publicURL, plain, out.Workspace)
	return out, nil
}

func (h *MigrationHandler) overview(wsID *uint) (*MigrationOverview, error) {
	out := &MigrationOverview{Enabled: h.enabled, URL: h.publicURL, Exports: []MigrationExportSummary{}}
	open, err := h.activeWindows(wsID)
	if err != nil {
		return nil, err
	}
	if len(open) > 0 {
		k := open[0]
		out.Window = &MigrationWindow{StartedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt}
	}
	scope := uint(0)
	if wsID != nil {
		scope = *wsID
		if ws, err := h.workspaces.FindByID(*wsID); err == nil && ws.Restriction == models.WorkspaceRestrictionFrozen {
			out.Frozen, out.FrozenReason = true, ws.RestrictionReason
		}
	}
	runs, err := h.svc.MigrationExports(scope, 20)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		e := MigrationExportSummary{ID: r.ID, Workspace: r.SourceWorkspace, Status: r.Status, Phase: r.Phase, Error: r.Error, CreatedAt: r.CreatedAt}
		if m := r.Migration; m != nil {
			e.Freeze, e.Data, e.Completed, e.ImportedBy = m.Freeze, m.Data, m.Completed, m.ImportedBy
		}
		out.Exports = append(out.Exports, e)
	}
	return out, nil
}

// WorkspaceMigration shows the workspace's open migration, its pulls, and whether it is frozen. Owner only.
func (h *MigrationHandler) WorkspaceMigration(c *okapi.Context) error {
	wsID := uint(middlewares.WorkspaceID(c))
	out, err := h.overview(&wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to load the migration", err)
	}
	return ok(c, out)
}

// StartWorkspaceMigration opens a migration of this workspace and returns its code. Owner only.
func (h *MigrationHandler) StartWorkspaceMigration(c *okapi.Context, req *MigrationStartRequest) error {
	wsID := uint(middlewares.WorkspaceID(c))
	out, err := h.startFor(uint(middlewares.UserID(c)), &wsID, req.Body.ExpiresInHours)
	if err != nil {
		return moveAbort(c, err, "failed to start the migration")
	}
	h.record(c, wsID, "workspace.migration_start", out.Workspace, map[string]any{"expires_at": out.ExpiresAt})
	return ok(c, out)
}

// CancelWorkspaceMigration closes the workspace's open migration: its code stops working.
func (h *MigrationHandler) CancelWorkspaceMigration(c *okapi.Context) error {
	wsID := uint(middlewares.WorkspaceID(c))
	if err := h.closeWindows(&wsID); err != nil {
		return c.AbortInternalServerError("failed to cancel the migration", err)
	}
	h.record(c, wsID, "workspace.migration_cancel", "", nil)
	return ok(c, map[string]any{"cancelled": true})
}

// InstanceMigration shows the install's open migration and every pull. Platform admin only.
func (h *MigrationHandler) InstanceMigration(c *okapi.Context) error {
	out, err := h.overview(nil)
	if err != nil {
		return c.AbortInternalServerError("failed to load the migration", err)
	}
	return ok(c, out)
}

// StartInstanceMigration opens a migration of every workspace on the install. Platform admin only.
func (h *MigrationHandler) StartInstanceMigration(c *okapi.Context, req *MigrationStartRequest) error {
	out, err := h.startFor(uint(middlewares.UserID(c)), nil, req.Body.ExpiresInHours)
	if err != nil {
		return moveAbort(c, err, "failed to start the migration")
	}
	h.record(c, 0, "admin.migration_start", "", map[string]any{"expires_at": out.ExpiresAt})
	return ok(c, out)
}

// CancelInstanceMigration closes the install's open migration.
func (h *MigrationHandler) CancelInstanceMigration(c *okapi.Context) error {
	if err := h.closeWindows(nil); err != nil {
		return c.AbortInternalServerError("failed to cancel the migration", err)
	}
	h.record(c, 0, "admin.migration_cancel", "", nil)
	return ok(c, map[string]any{"cancelled": true})
}

// UnfreezeWorkspace lets the source's owner roll a migration back from this install.
func (h *MigrationHandler) UnfreezeWorkspace(c *okapi.Context) error {
	wsID := uint(middlewares.WorkspaceID(c))
	if err := h.svc.UnfreezeMigration(c.Request().Context(), wsID); err != nil {
		return moveAbort(c, err, "failed to unfreeze the workspace")
	}
	h.record(c, wsID, "workspace.migration_unfreeze", "", nil)
	return ok(c, map[string]any{"unfrozen": true})
}

// MigrationSourceRequest identifies a source install and the key it issued.
type MigrationSourceRequest struct {
	Body wsbackup.MigrationSource `json:"body"`
}

// Probe checks a source and key, and lists the workspaces the key opens.
func (h *MigrationHandler) Probe(c *okapi.Context, req *MigrationSourceRequest) error {
	if !h.enabled {
		return c.AbortWithError(403, errMigrationOff)
	}
	info, err := h.svc.ProbeMigrationSource(c.Request().Context(), req.Body)
	if err != nil {
		return moveAbort(c, err, "failed to reach the source")
	}
	return ok(c, info)
}

// MigrationImportBody is one workspace to pull.
type MigrationImportBody struct {
	wsbackup.MigrationSource
	SourceWorkspace string `json:"source_workspace" doc:"The workspace's name on the source; optional when the key opens one"`
	NewWorkspace    string `json:"new_workspace" doc:"The workspace created here; the source's name when omitted"`
	RestoreData     bool   `json:"restore_data"`
	DeployApps      bool   `json:"deploy_apps"`
	Freeze          bool   `json:"freeze" doc:"Freeze the source before the copy, for a consistent cut-over"`
	Rehearse        bool   `json:"rehearse" doc:"Configuration only: no data, freeze or deploy"`
	RetireSource    bool   `json:"retire_source" doc:"Mark the source workspace as migrated here once the import succeeds"`
}

type MigrationImportRequest struct {
	Body MigrationImportBody `json:"body"`
}

func (b MigrationImportBody) input(batch string) wsbackup.MigrationImportInput {
	return wsbackup.MigrationImportInput{
		Source: b.MigrationSource, SourceWorkspace: b.SourceWorkspace, NewWorkspaceName: b.NewWorkspace,
		RestoreData: b.RestoreData, DeployApps: b.DeployApps, Freeze: b.Freeze, Rehearse: b.Rehearse,
		RetireSource: b.RetireSource, BatchID: batch,
	}
}

// Import creates a workspace here and pulls the source workspace into it. The caller owns the new workspace.
func (h *MigrationHandler) Import(c *okapi.Context, req *MigrationImportRequest) error {
	if !h.enabled {
		return c.AbortWithError(403, errMigrationOff)
	}
	run, err := h.svc.StartMigrationImport(c.Request().Context(), uint(middlewares.UserID(c)), req.Body.input(""))
	if err != nil {
		return moveAbort(c, err, "failed to start the migration")
	}
	h.recordImport(c, run, req.Body)
	return ok(c, run)
}

func (h *MigrationHandler) recordImport(c *okapi.Context, run *models.WorkspaceBundle, b MigrationImportBody) {
	h.record(c, run.WorkspaceID, "workspace.migration_import", run.Ref, map[string]any{
		"source_url": b.URL, "source_workspace": run.SourceWorkspace, "insecure_skip_tls_verify": b.InsecureTLS,
		"freeze": b.Freeze, "rehearse": b.Rehearse, "restore_data": b.RestoreData,
	})
}

// ListImports lists the caller's migrations into this install.
func (h *MigrationHandler) ListImports(c *okapi.Context) error {
	runs, err := h.svc.Repo.ListByKindForUser(models.BundleMigrationImport, uint(middlewares.UserID(c)), 100)
	if err != nil {
		return c.AbortInternalServerError("failed to list migrations", err)
	}
	return ok(c, runs)
}

type MigrationRunRef struct {
	ID uint `path:"id" required:"true"`
}

// GetImport shows one of the caller's migrations; a platform admin sees any.
func (h *MigrationHandler) GetImport(c *okapi.Context, req *MigrationRunRef) error {
	run, err := h.svc.Repo.FindByID(req.ID)
	if err != nil || run.Kind != models.BundleMigrationImport {
		return c.AbortNotFound("no such migration")
	}
	if run.CreatedBy == nil || *run.CreatedBy != uint(middlewares.UserID(c)) {
		if u, err := h.keyUser(c); err != nil || !u.IsAdmin() {
			return c.AbortNotFound("no such migration")
		}
	}
	return ok(c, run)
}

// MigrationBatchRequest pulls several workspaces from one source with an instance key.
type MigrationBatchRequest struct {
	Body struct {
		wsbackup.MigrationSource
		Workspaces   []string `json:"workspaces" doc:"Source workspace names; every workspace the key opens when empty"`
		RestoreData  bool     `json:"restore_data"`
		DeployApps   bool     `json:"deploy_apps"`
		Freeze       bool     `json:"freeze"`
		RetireSource bool     `json:"retire_source"`
	} `json:"body"`
}

// MigrationBatch is what a batch started: one run per workspace, or why it could not.
type MigrationBatch struct {
	BatchID string                   `json:"batch_id"`
	Runs    []models.WorkspaceBundle `json:"runs"`
	Errors  map[string]string        `json:"errors,omitempty"`
}

// ImportBatch starts one migration per source workspace. Platform admin only; the admin owns the new
// workspaces, and their members come across by email.
func (h *MigrationHandler) ImportBatch(c *okapi.Context, req *MigrationBatchRequest) error {
	if !h.enabled {
		return c.AbortWithError(403, errMigrationOff)
	}
	ctx := c.Request().Context()
	info, err := h.svc.ProbeMigrationSource(ctx, req.Body.MigrationSource)
	if err != nil {
		return moveAbort(c, err, "failed to reach the source")
	}
	names := req.Body.Workspaces
	if len(names) == 0 {
		for _, w := range info.Workspaces {
			names = append(names, w.Name)
		}
	}
	batch := MigrationBatch{BatchID: uuid.NewString(), Runs: []models.WorkspaceBundle{}, Errors: map[string]string{}}
	for _, name := range names {
		body := MigrationImportBody{MigrationSource: req.Body.MigrationSource, SourceWorkspace: name,
			RestoreData: req.Body.RestoreData, DeployApps: req.Body.DeployApps, Freeze: req.Body.Freeze, RetireSource: req.Body.RetireSource}
		run, err := h.svc.StartMigrationImport(ctx, uint(middlewares.UserID(c)), body.input(batch.BatchID))
		if err != nil {
			batch.Errors[name] = err.Error()
			continue
		}
		h.recordImport(c, run, body)
		batch.Runs = append(batch.Runs, *run)
	}
	return ok(c, batch)
}

// ListAllImports lists every migration into this install. Platform admin only.
func (h *MigrationHandler) ListAllImports(c *okapi.Context) error {
	runs, err := h.svc.Repo.ListByKind(models.BundleMigrationImport, 200)
	if err != nil {
		return c.AbortInternalServerError("failed to list migrations", err)
	}
	return ok(c, runs)
}

func (h *MigrationHandler) record(c *okapi.Context, wsID uint, action, target string, meta map[string]any) {
	actor := uint(middlewares.UserID(c))
	e := audit.Entry{ActorID: &actor, Action: action, TargetType: "migration", TargetID: target, IP: c.RealIP(), Metadata: meta}
	if wsID != 0 {
		e.WorkspaceID = &wsID
	}
	h.audit.Record(e)
}

// moveAbort maps the service's errors onto the response envelope.
func moveAbort(c *okapi.Context, err error, fallback string) error {
	var me migrationError
	switch {
	case errors.As(err, &me):
		return c.AbortWithError(403, err)
	case errors.Is(err, wsbackup.ErrBusy):
		return c.AbortWithError(409, err)
	case errors.Is(err, wsbackup.ErrExportNotFound), errors.Is(err, wsbackup.ErrNotMigrationExport), errors.Is(err, wsbackup.ErrNoArtifact):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, wsbackup.ErrExportNotReady), errors.Is(err, wsbackup.ErrExportPurged):
		return c.AbortWithError(409, err)
	case errors.Is(err, wsbackup.ErrSourceWorkspace), errors.Is(err, wsbackup.ErrBundleTooNew), errors.Is(err, wsbackup.ErrBadMigrationCode):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, wsbackup.ErrSourceUnreachable):
		return c.AbortWithError(http.StatusBadGateway, err)
	case errors.Is(err, workspace.ErrWorkspaceCreationNotAllowed), errors.Is(err, workspace.ErrWorkspaceLimitReached),
		errors.Is(err, workspace.ErrOrgWorkspaceLimitReached):
		return c.AbortWithError(403, err)
	case strings.Contains(err.Error(), "the source refused"), strings.Contains(err.Error(), "source URL"),
		strings.Contains(err.Error(), "the key opens"), strings.Contains(err.Error(), "CA bundle"), strings.Contains(err.Error(), "no migration API"):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError(fallback, err)
	}
}
