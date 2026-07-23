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

// applyRoutes registers the declarative apply API (dry-run plan + converge).
func (r *Router) applyRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Apply", Description: "Declarative apply: preview or converge a workspace to a bundle of miabi.io/v1 manifests."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodPost,
			Path:        "/{workspace}/apply",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.apply.Apply),
			Summary:     "Apply or preview a manifest bundle",
			Request:     &handlers.ApplyRequest{},
		},
	}
}

// gitOpsRoutes registers GitSource CRUD, sync, diff, and the inbound webhook.
func (r *Router) gitOpsRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "GitOps", Description: "Declarative continuous deployment from Git repositories of miabi.io/v1 manifests."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/gitops"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.gitops.List,
			Summary:     "List git sources",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.gitops.Create),
			Summary:     "Create a git source",
			Request:     &handlers.CreateGitSourceRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{gitSourceID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.gitops.Get,
			Summary:     "Get a git source",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{gitSourceID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.gitops.Update),
			Summary:     "Update a git source",
			Request:     &handlers.UpdateGitSourceRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{gitSourceID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.gitops.Delete,
			Summary:     "Delete a git source",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{gitSourceID}/sync",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.gitops.Sync,
			Summary:     "Reconcile a git source now",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{gitSourceID}/resources/{kind}/{name}/sync",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.gitops.SyncResource,
			Summary:     "Reconcile a single managed resource",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{gitSourceID}/resources/{kind}/{name}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.gitops.DeleteResource,
			Summary:     "Delete a single managed live resource",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{gitSourceID}/diff",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.gitops.Diff,
			Summary:     "Desired-vs-live diff",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{gitSourceID}/topology",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.gitops.Topology,
			Summary:     "Resource topology graph (nodes + edges)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{gitSourceID}/status",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.gitops.Status,
			Summary:     "Live runtime status of managed resources",
		},
		// Public push webhook: authenticated by the source's webhook secret, not a session.
		{
			Method:  http.MethodPost,
			Path:    base + "/{gitSourceID}/webhook",
			Group:   g,
			Handler: r.h.gitops.Webhook,
			Summary: "Provider push webhook",
			Options: []okapi.RouteOption{okapi.DocHide()},
		},
	}
}

// pipelineRoutes registers pipeline CRUD, run triggering, history, and logs.
func (r *Router) pipelineRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Pipelines", Description: "CI/CD pipelines: build, test, and deploy on the internal runner."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/pipelines"
	const runs = "/{workspace}/pipeline-runs"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.pipeline.List,
			Summary:     "List pipelines",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.pipeline.Create),
			Summary:     "Create a pipeline",
			Request:     &handlers.CreatePipelineRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{pipelineID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.pipeline.Get,
			Summary:     "Get a pipeline",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{pipelineID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.pipeline.Update),
			Summary:     "Update a pipeline",
			Request:     &handlers.UpdatePipelineRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{pipelineID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.pipeline.Delete,
			Summary:     "Delete a pipeline",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{pipelineID}/trigger",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.pipeline.Trigger),
			Summary:     "Trigger a pipeline run",
			Request:     &handlers.TriggerPipelineRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{pipelineID}/runs",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.pipeline.ListRuns,
			Summary:     "List pipeline runs",
		},
		{
			Method:      http.MethodGet,
			Path:        runs + "/{runID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.pipeline.GetRun,
			Summary:     "Get a pipeline run",
		},
		{
			Method:      http.MethodGet,
			Path:        runs + "/{runID}/logs",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.pipeline.RunLogs,
			Summary:     "Stream pipeline run logs (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        runs + "/{runID}/logs/history",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.pipeline.RunLogsHistory,
			Summary:     "Get a pipeline run's full stored per-step logs (JSON)",
		},
		{
			Method:      http.MethodGet,
			Path:        runs + "/{runID}/steps/{ordinal}/logs/download",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.pipeline.StepLogsDownload,
			Summary:     "Download a pipeline step's full logs",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{pipelineID}/webhook-info",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.pipeline.WebhookInfo,
			Summary:     "Reveal the push-webhook URL + secret",
		},
		// Public push webhook: authenticated by the pipeline's webhook secret, not a session.
		{
			Method:  http.MethodPost,
			Path:    base + "/{pipelineID}/webhook",
			Group:   g,
			Handler: r.h.pipeline.Webhook,
			Summary: "Provider push webhook",
			Options: []okapi.RouteOption{okapi.DocHide()},
		},
	}
}

// imageRoutes registers the built-image catalog: provenance listing + deletion.
func (r *Router) imageRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Images", Description: "Built-image catalog: artifacts produced by pipeline runs, with provenance."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/images"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.image.List,
			Summary:     "List built images (optionally ?app=<id>)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{imageID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.image.Get,
			Summary:     "Get a built image",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{imageID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.image.Delete,
			Summary:     "Delete a built image",
		},
	}
}
