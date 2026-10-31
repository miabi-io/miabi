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
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
)

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
		{
			Method: http.MethodGet,
			Path:   "/{workspace}/apps/{appID}/manifest",
			Group:  g,
			// Viewer: the bundle carries no secret values — a secret env var is emitted as its name
			// under secretEnv with an empty value, exactly as the plan engine masks it.
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.apply.ExportApplication,
			Summary:     "Export an application as a miabi.io/v1 manifest bundle",
		},
	}
}

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

// versioningRoutes serve version policies: how pipeline-built images are numbered and tagged. Reading is
// open to viewers; changing the rules, or the series a repository releases on, is an admin decision.
func (r *Router) versioningRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Versioning", Description: "Version policies: the version, and image tag, each pipeline build gets."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/versioning/policies"

	return []okapi.RouteDefinition{
		{Method: http.MethodGet, Path: base, Group: g, Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler: r.h.versioning.List, Summary: "List version policies"},
		{Method: http.MethodPost, Path: base, Group: g, Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler: okapi.H(r.h.versioning.Create), Summary: "Create a version policy for a pipeline's image repository",
			Request: &handlers.CreateVersionPolicyRequest{}},
		{Method: http.MethodPost, Path: "/{workspace}/versioning/preview", Group: g, Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler: okapi.H(r.h.versioning.PreviewSettings), Summary: "Preview the versions unsaved settings would produce",
			Request: &handlers.PreviewVersionsRequest{}},
		{Method: http.MethodGet, Path: base + "/{policyID}", Group: g, Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler: r.h.versioning.Get, Summary: "Get a version policy"},
		{Method: http.MethodPut, Path: base + "/{policyID}", Group: g, Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler: okapi.H(r.h.versioning.Update), Summary: "Replace a version policy's settings",
			Request: &handlers.UpdateVersionPolicyRequest{}},
		{Method: http.MethodDelete, Path: base + "/{policyID}", Group: g, Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler: r.h.versioning.Delete, Summary: "Delete a version policy"},
		{Method: http.MethodPost, Path: base + "/{policyID}/switch-series", Group: g, Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler: okapi.H(r.h.versioning.SwitchSeries), Summary: "Switch the series a policy releases on",
			Request: &handlers.SwitchSeriesRequest{}},
		{Method: http.MethodGet, Path: base + "/{policyID}/allocations", Group: g, Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler: r.h.versioning.Allocations, Summary: "List the versions a policy handed out"},
		{Method: http.MethodGet, Path: base + "/{policyID}/preview", Group: g, Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler: r.h.versioning.Preview, Summary: "Preview the next versions a policy will hand out"},
		{Method: http.MethodGet, Path: "/{workspace}/pipelines/{pipelineID}/versioning", Group: g, Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler: r.h.versioning.ForPipeline, Summary: "Get the version policy covering a pipeline's image repository"},
	}
}

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
			Path:        base + "/runs/stream",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.pipeline.StreamWorkspaceRuns,
			Summary:     "Stream pipeline run transitions (SSE)",
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
			Method:      http.MethodPost,
			Path:        runs + "/{runID}/rerun",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.pipeline.Rerun),
			Summary:     "Re-run a pipeline run",
			Request:     &handlers.RerunPipelineRequest{},
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
