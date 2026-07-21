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

// monitoringRoutes registers workspace overview and per-app metrics (incl. SSE).
func (r *Router) monitoringRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Monitoring", Description: "Workspace overview and application metrics."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/overview",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.monitoring.Overview,
			Summary:     "Workspace health & resource overview",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/usage/live",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.monitoring.WorkspaceUsageLive,
			Summary:     "Live workspace resource usage (aggregated container stats, snapshot)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/usage/live/stream",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.monitoring.WorkspaceUsageStream,
			Summary:     "Live workspace resource usage (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/usage/history",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.monitoring.WorkspaceUsageHistory,
			Summary:     "Workspace resource usage history (aggregated scraper samples)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/apps/{appID}/metrics",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.monitoring.AppMetrics,
			Summary:     "App resource metrics (snapshot)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/apps/{appID}/metrics/stream",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.monitoring.AppMetricsStream,
			Summary:     "App resource metrics (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/apps/{appID}/metrics/history",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.monitoring.AppMetricsHistory,
			Summary:     "App resource metrics history",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/analytics",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.analytics.Report,
			Summary:     "Workspace analytics (HTTP traffic, performance, web) — ?range=&app=",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/analytics/apps",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.analytics.Apps,
			Summary:     "Applications with analytics data in the window (for the app filter)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/analytics/export",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.analytics.Export,
			Summary:     "Export analytics time series as CSV (Enterprise) — ?range=&app=",
		},
	}
}

// marketplaceRoutes registers the catalog (read) and workspace install.
func (r *Router) marketplaceRoutes() []okapi.RouteDefinition {
	cat := r.v1.Group("/marketplace").WithTagInfo(okapi.GroupTag{Name: "Marketplace", Description: "One-click application & database templates."})
	ws := r.v1.Group("/workspaces")
	scopedDev := []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleDeveloper)}
	scopedViewer := []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleViewer)}
	scopedAdmin := []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleAdmin)}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/templates",
			Group:       cat,
			Middlewares: []okapi.Middleware{r.authenticate},
			Handler:     r.h.marketplace.ListTemplates,
			Summary:     "List marketplace templates",
		},
		{
			Method:      http.MethodGet,
			Path:        "/templates/{name}",
			Group:       cat,
			Middlewares: []okapi.Middleware{r.authenticate},
			Handler:     r.h.marketplace.GetTemplate,
			Summary:     "Get a template",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/marketplace/templates",
			Group:       ws,
			Middlewares: scopedViewer,
			Handler:     r.h.marketplace.ListWorkspaceTemplates,
			Summary:     "List templates (official + custom)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/marketplace/templates/{name}",
			Group:       ws,
			Middlewares: scopedViewer,
			Handler:     r.h.marketplace.GetWorkspaceTemplate,
			Summary:     "Get a template (workspace-scoped)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{workspace}/marketplace/templates/import",
			Group:       ws,
			Middlewares: scopedDev,
			Handler:     okapi.H(r.h.marketplace.ImportTemplate),
			Summary:     "Import a custom template",
			Request:     &handlers.ImportRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/marketplace/templates/{name}/raw",
			Group:       ws,
			Middlewares: scopedDev,
			Handler:     r.h.marketplace.GetCustomTemplateYAML,
			Summary:     "Get a custom template's manifest YAML",
		},
		{
			Method:      http.MethodPut,
			Path:        "/{workspace}/marketplace/templates/{name}",
			Group:       ws,
			Middlewares: scopedDev,
			Handler:     okapi.H(r.h.marketplace.UpdateTemplate),
			Summary:     "Update a custom template",
			Request:     &handlers.UpdateTemplateRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{workspace}/marketplace/templates/{name}",
			Group:       ws,
			Middlewares: scopedDev,
			Handler:     r.h.marketplace.DeleteTemplate,
			Summary:     "Delete a custom template",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{workspace}/marketplace/install",
			Group:       ws,
			Middlewares: scopedDev,
			Handler:     okapi.H(r.h.marketplace.Install),
			Summary:     "Install a template",
			Request:     &handlers.InstallRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{workspace}/marketplace/install/jobs",
			Group:       ws,
			Middlewares: scopedDev,
			Handler:     okapi.H(r.h.marketplace.StartInstall),
			Summary:     "Start an async install (live progress via SSE)",
			Request:     &handlers.InstallRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/marketplace/install/jobs/{jobID}",
			Group:       ws,
			Middlewares: scopedViewer,
			Handler:     r.h.marketplace.InstallJob,
			Summary:     "Get an install job snapshot",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/marketplace/install/jobs/{jobID}/events",
			Group:       ws,
			Middlewares: scopedViewer,
			Handler:     r.h.marketplace.InstallJobEvents,
			Summary:     "Stream install progress (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/marketplace/installs",
			Group:       ws,
			Middlewares: scopedViewer,
			Handler:     r.h.marketplace.ListInstalls,
			Summary:     "List template installs",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/marketplace/installs/{installID}/upgrade/plan",
			Group:       ws,
			Middlewares: scopedDev,
			Handler:     r.h.marketplace.UpgradePlan,
			Summary:     "Preview an install upgrade (diff; ?version= targets a version)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{workspace}/marketplace/installs/{installID}/upgrade",
			Group:       ws,
			Middlewares: scopedDev,
			Handler:     okapi.H(r.h.marketplace.Upgrade),
			Summary:     "Upgrade an install to a newer version",
			Request:     &handlers.UpgradeRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{workspace}/marketplace/installs/{installID}",
			Group:       ws,
			Middlewares: scopedAdmin,
			Handler:     r.h.marketplace.Uninstall,
			Summary:     "Uninstall a template",
		},
	}
}
