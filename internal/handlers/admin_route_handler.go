// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// AdminRouteHandler exposes platform-wide route administration: a searchable list
// of every workspace's routes with their gateway sync status, and a one-click
// resync that re-renders every workspace's gateway config from the database.
type AdminRouteHandler struct {
	db         *gorm.DB
	routes     *repositories.RouteRepository
	workspaces *repositories.WorkspaceRepository
	svc        *route.Service
	audit      *audit.Logger
}

func NewAdminRouteHandler(db *gorm.DB, routes *repositories.RouteRepository, workspaces *repositories.WorkspaceRepository, svc *route.Service, auditLog *audit.Logger) *AdminRouteHandler {
	return &AdminRouteHandler{db: db, routes: routes, workspaces: workspaces, svc: svc, audit: auditLog}
}

// AdminRoute is one row in the platform-wide routes list.
type AdminRoute struct {
	ID            uint                `json:"id"`
	WorkspaceID   uint                `json:"workspace_id"`
	WorkspaceName string              `json:"workspace_name"`
	ApplicationID uint                `json:"application_id"`
	AppName       string              `json:"app_name"`
	Name          string              `json:"name"`
	Path          string              `json:"path"`
	Hosts         []string            `json:"hosts,omitempty"`
	TLSMode       models.RouteTLSMode `json:"tls_mode"`
	Enabled       bool                `json:"enabled"`
	Generated     bool                `json:"generated"`
	Status        models.RouteStatus  `json:"status"`
	StatusReason  string              `json:"status_reason,omitempty"`
	SyncedAt      *time.Time          `json:"synced_at,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
}

// List returns a page of routes across every workspace, with workspace and app
// names resolved, filterable by status, workspace, and a name/host search.
func (h *AdminRouteHandler) List(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	var wsFilter *uint
	if v := queryInt(c, "workspace", 0); v > 0 {
		w := uint(v)
		wsFilter = &w
	}
	routes, total, err := h.routes.ListPagedAll(c.Query("search"), c.Query("status"), wsFilter, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list routes", err)
	}
	wsNames := h.workspaceNames(routes)
	appNames := h.appNames(routes)
	out := make([]AdminRoute, 0, len(routes))
	for i := range routes {
		rt := &routes[i]
		out = append(out, AdminRoute{
			ID: rt.ID, WorkspaceID: rt.WorkspaceID, WorkspaceName: wsNames[rt.WorkspaceID],
			ApplicationID: rt.ApplicationID, AppName: appNames[rt.ApplicationID],
			Name: rt.Name, Path: rt.Path, Hosts: rt.Hosts, TLSMode: rt.TLSMode,
			Enabled: rt.Enabled, Generated: rt.Generated, Status: rt.Status,
			StatusReason: rt.StatusReason, SyncedAt: rt.SyncedAt, CreatedAt: rt.CreatedAt,
		})
	}
	return paginated(c, out, total, page, size)
}

// ResyncSummary reports the outcome of a platform-wide route resync.
type ResyncSummary struct {
	Workspaces int                    `json:"workspaces"` // reconciled successfully
	Failed     int                    `json:"failed"`     // workspaces whose reconcile errored
	Routes     int                    `json:"routes"`     // total routes
	Live       int                    `json:"live"`
	Offline    int                    `json:"offline"`
	Errors     int                    `json:"errors"`  // routes left in error status
	Pending    int                    `json:"pending"` // routes never reconciled
	FailedList []ResyncWorkspaceError `json:"failed_list,omitempty"`
}

// ResyncWorkspaceError names a workspace whose gateway config could not be rewritten.
type ResyncWorkspaceError struct {
	WorkspaceID uint   `json:"workspace_id"`
	Name        string `json:"name"`
	Error       string `json:"error"`
}

// Resync re-renders every workspace's gateway config file from current database state. Each
// file is replaced atomically (no directory wipe), so the gateway never sees an empty config.
// Per-route sync status is committed per workspace, so the summary reflects the post-resync state.
func (h *AdminRouteHandler) Resync(c *okapi.Context) error {
	workspaces, err := h.workspaces.ListAll()
	if err != nil {
		return c.AbortInternalServerError("failed to list workspaces", err)
	}
	ctx := c.Request().Context()
	summary := ResyncSummary{}
	for i := range workspaces {
		ws := &workspaces[i]
		if err := h.svc.SyncWorkspaceProxy(ctx, ws.ID); err != nil {
			summary.Failed++
			summary.FailedList = append(summary.FailedList, ResyncWorkspaceError{WorkspaceID: ws.ID, Name: ws.DisplayName, Error: err.Error()})
			continue
		}
		summary.Workspaces++
	}

	// Read back the committed per-route statuses for the summary counts.
	counts, err := h.routes.CountByStatus()
	if err == nil {
		summary.Live = int(counts[models.RouteStatusLive])
		summary.Offline = int(counts[models.RouteStatusOffline])
		summary.Errors = int(counts[models.RouteStatusError])
		summary.Pending = int(counts[models.RouteStatusPending])
		summary.Routes = summary.Live + summary.Offline + summary.Errors + summary.Pending
	}

	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID:    &actor,
		Action:     "admin.routes.resync",
		TargetType: "route",
		IP:         c.RealIP(),
		Metadata:   map[string]any{"workspaces": summary.Workspaces, "failed": summary.Failed, "routes": summary.Routes},
	})
	return ok(c, summary)
}

func (h *AdminRouteHandler) workspaceNames(routes []models.Route) map[uint]string {
	ids := distinct(routes, func(r *models.Route) uint { return r.WorkspaceID })
	m := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return m
	}
	var ws []models.Workspace
	h.db.Where("id IN ?", ids).Find(&ws)
	for i := range ws {
		m[ws[i].ID] = ws[i].DisplayName
	}
	return m
}

func (h *AdminRouteHandler) appNames(routes []models.Route) map[uint]string {
	ids := distinct(routes, func(r *models.Route) uint { return r.ApplicationID })
	m := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return m
	}
	var apps []models.Application
	h.db.Where("id IN ?", ids).Find(&apps)
	for i := range apps {
		m[apps[i].ID] = apps[i].Name
	}
	return m
}

func distinct(routes []models.Route, key func(*models.Route) uint) []uint {
	seen := map[uint]bool{}
	var ids []uint
	for i := range routes {
		if id := key(&routes[i]); id != 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}
