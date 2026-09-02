// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
)

// applicationRoutes registers workspace-scoped application and deployment routes.
// Viewer reads; Developer deploys and edits; Admin deletes.
func (r *Router) applicationRoutes() []okapi.RouteDefinition {
	apps := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Applications", Description: "Applications, env vars, deployments, and releases."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	// appOp gates a per-app operation on a permission that may be held workspace-wide (the normal
	// role) OR granted on this specific app by a resource policy (Enterprise). Additive for
	// per-resource grantees, and identical to the plain role check in Community.
	appOp := func(perm models.Permission) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireResourcePermission(perm, models.ResourceTypeApp, "appID", r.resourcePolicies)}
	}
	const base = "/{workspace}/apps"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.List,
			Summary:     "List applications",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/resource-limits",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.ResourceLimits,
			Summary:     "Get platform per-app resource caps",
			Response:    &dto.Response[handlers.ResourceLimitsResponse]{},
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.app.Create),
			Summary:     "Create an application",
			Request:     &handlers.CreateAppRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.Get,
			Summary:     "Get an application",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/overview",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.Overview,
			Summary:     "Get application overview (summary + counts)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/status",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.Status,
			Summary:     "Get live container status (+ stats snapshot)",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{appID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.app.Update),
			Summary:     "Update an application",
			Request:     &handlers.UpdateAppRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{appID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.app.Delete,
			Summary:     "Delete an application",
		},

		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/env",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.ListEnvVars,
			Summary:     "List env vars",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{appID}/env",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.app.SetEnvVar),
			Summary:     "Set an env var",
			Request:     &handlers.SetEnvVarRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/env/import",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.app.ImportEnvVars),
			Summary:     "Bulk-import env vars from .env",
			Request:     &handlers.ImportEnvVarsRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{appID}/env/{key}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.app.DeleteEnvVar,
			Summary:     "Delete an env var",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/env/{key}/reveal",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.app.RevealEnvVar,
			Summary:     "Reveal a secret env var's value (admin)",
		},

		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/labels",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.ListLabels,
			Summary:     "List custom container labels",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{appID}/labels",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.app.SetLabels),
			Summary:     "Replace custom container labels",
			Request:     &handlers.SetLabelsRequest{},
		},

		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/databases",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.database.ListByApp,
			Summary:     "List databases attached to the app",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/databases/{dbID}/connection",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.AppDatabaseConnection,
			Summary:     "Reveal an attached database connection",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{appID}/databases/{dbID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.database.AttachToApp),
			Summary:     "Attach an existing database to the app",
			Request:     &handlers.AttachDatabaseRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{appID}/databases/{dbID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.database.DetachFromApp,
			Summary:     "Detach a database from the app",
		},

		{
			Method:      http.MethodPut,
			Path:        base + "/{appID}/volumes",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.app.AttachVolume),
			Summary:     "Attach a volume",
			Request:     &handlers.AttachVolumeRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{appID}/volumes/{volumeID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.app.DetachVolume,
			Summary:     "Detach a volume",
		},

		{
			Method:      http.MethodPut,
			Path:        base + "/{appID}/configs",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.app.AttachConfig),
			Summary:     "Mount a config file set",
			Request:     &handlers.AttachConfigRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{appID}/configs/{configID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.app.DetachConfig,
			Summary:     "Remove a config file mount",
		},

		// One-click external access: auto-generated public hostnames + Gateway routes.
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/external-access",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.route.ExternalAccess,
			Summary:     "Get the app's external-access state",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{appID}/external-access",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.route.SetExternalAccess),
			Summary:     "Expose/unexpose app ports externally",
			Request:     &handlers.SetExternalAccessRequest{},
		},

		// Privileged host binds (allow-listed; Admin-only, privileged workspace only).
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/host-mount-presets",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.HostMountPresets,
			Summary:     "List allow-listed host mount presets",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{appID}/host-mounts",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.app.AttachHostMount),
			Summary:     "Attach a privileged host mount",
			Request:     &handlers.AttachHostMountRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{appID}/host-mounts/{preset}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.app.DetachHostMount,
			Summary:     "Detach a privileged host mount",
		},

		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/deploy",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     okapi.H(r.h.app.Deploy),
			Summary:     "Deploy the application",
			Request:     &handlers.DeployRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/build-cache/invalidate",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     r.h.app.InvalidateBuildCache,
			Summary:     "Invalidate the application's build cache",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/start",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     r.h.app.Start,
			Summary:     "Start the application container",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/stop",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     r.h.app.Stop,
			Summary:     "Stop the application container",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/restart",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     r.h.app.Restart,
			Summary:     "Restart the application container",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{appID}/source",
			Group:       apps,
			Middlewares: appOp(models.PermAppWrite),
			Handler:     okapi.H(r.h.app.SetSource),
			Summary:     "Replace the application's source (switch between image and git)",
			Request:     &handlers.SetSourceRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/pipeline/resync",
			Group:       apps,
			Middlewares: appOp(models.PermAppWrite),
			Handler:     okapi.H(r.h.app.ResyncPipeline),
			Summary:     "Reload the repository's pipelines.yaml for a git application",
			Request:     &handlers.ResyncPipelineRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/scale",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     okapi.H(r.h.app.Scale),
			Summary:     "Scale a cluster (service) application",
			Request:     &handlers.ScaleRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/rollback",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     okapi.H(r.h.app.Rollback),
			Summary:     "Roll back to a release",
			Request:     &handlers.RollbackRequest{},
		},

		// Canary deployment: run a new version alongside the stable one and shift traffic.
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/canary",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     okapi.H(r.h.app.StartCanary),
			Summary:     "Start a canary deployment",
			Request:     &handlers.StartCanaryRequest{},
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{appID}/canary",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     okapi.H(r.h.app.SetCanaryWeight),
			Summary:     "Set canary traffic weight",
			Request:     &handlers.CanaryWeightRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/canary/promote",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     r.h.app.PromoteCanary,
			Summary:     "Promote canary to stable",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{appID}/canary",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     r.h.app.AbortCanary,
			Summary:     "Abort the canary deployment",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{appID}/canary/routing",
			Group:       apps,
			Middlewares: appOp(models.PermAppDeploy),
			Handler:     okapi.H(r.h.app.SetCanaryRouting),
			Summary:     "Set canary mode and match rules (Enterprise beyond the automatic ramp)",
			Request:     &handlers.CanaryRoutingRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/canary/preview",
			Group:       apps,
			Middlewares: appOp(models.PermAppRead),
			Handler:     okapi.H(r.h.app.PreviewCanaryRouting),
			Summary:     "Preview which backend would serve a request",
			Request:     &handlers.CanaryPreviewRequest{},
		},

		// Per-resource access policies (Enterprise; gated resource_policies → 402 in CE).
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/policies",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.resourcePolicy.ListApp,
			Summary:     "List access policies on the app",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{appID}/policies",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.resourcePolicy.GrantApp),
			Summary:     "Grant a user permissions on the app",
			Request:     &handlers.GrantPolicyRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{appID}/policies/{userID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.resourcePolicy.RevokeApp,
			Summary:     "Revoke a user's app access policy",
		},

		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/deployments",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.ListDeployments,
			Summary:     "List deployments",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/deployments/{deploymentID}/logs",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.DeploymentLogs,
			Summary:     "Stream deployment logs (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/deployments/{deploymentID}/logs/history",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.DeploymentLogsHistory,
			Summary:     "Get a deployment's full stored logs (JSON)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/deployments/{deploymentID}/logs/download",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.DeploymentLogsDownload,
			Summary:     "Download deployment logs (full)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/events",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.events.List,
			Summary:     "List application events",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/events/stream",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.events.Stream,
			Summary:     "Stream application events (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/logs/stream",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.events.LogsStream,
			Summary:     "Stream runtime container logs (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/processes",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.Processes,
			Summary:     "List running processes in the container (docker top)",
		},
		// Interactive shell into the running container (WebSocket). Admin+ and
		// gated by the plan's shell-exec capability; auth via ?token=.
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/exec",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.app.ExecShell,
			Summary:     "Open an interactive shell in the container (WebSocket)",
		},

		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/releases",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.ListReleases,
			Summary:     "List releases",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{appID}/releases/{releaseID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.app.GetRelease,
			Summary:     "Get a release",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{appID}/releases/{releaseID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.app.PinRelease),
			Summary:     "Pin or unpin a release",
			Request:     &handlers.PinReleaseRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{appID}/releases/{releaseID}",
			Group:       apps,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.app.DeleteRelease,
			Summary:     "Delete a release",
		},
	}
}

// jobRoutes registers workspace-scoped Jobs and CronJobs. A job targets an application named
// in the request body but is managed at the workspace level. Viewer reads; Developer mutates.
func (r *Router) jobRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Jobs", Description: "One-off Jobs and CronJobs run in an application's runtime context."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base + "/jobs",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.job.List,
			Summary:     "List job runs (optionally ?app_id=)",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/jobs",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.job.Run),
			Summary:     "Run a one-off job",
			Request:     &handlers.RunJobRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/jobs/{jobID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.job.Get,
			Summary:     "Get a job run (incl. logs)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/jobs/{jobID}/logs/download",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.job.LogsDownload,
			Summary:     "Download a job's full logs",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/jobs/{jobID}/cancel",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.job.Cancel,
			Summary:     "Cancel a running job",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/jobs/{jobID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.job.Delete,
			Summary:     "Delete a job run",
		},

		// CronJobs: schedules that spawn Jobs.
		{
			Method:      http.MethodGet,
			Path:        base + "/cronjobs",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.job.ListCronJobs,
			Summary:     "List cronjobs (optionally ?app_id=)",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/cronjobs",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.job.CreateCronJob),
			Summary:     "Create a cronjob",
			Request:     &handlers.CronJobRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/cronjobs/{cronJobID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.job.GetCronJob,
			Summary:     "Get a cronjob",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/cronjobs/{cronJobID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.job.UpdateCronJob),
			Summary:     "Update a cronjob",
			Request:     &handlers.CronJobRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/cronjobs/{cronJobID}/run",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.job.RunCronJobNow,
			Summary:     "Trigger a cronjob run now",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/cronjobs/{cronJobID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.job.DeleteCronJob,
			Summary:     "Delete a cronjob",
		},
	}
}
