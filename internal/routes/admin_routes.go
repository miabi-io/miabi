// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/handlers"
)

// adminRoutes registers platform-admin management endpoints under /admin. Every
// route requires an authenticated platform super-admin.
func (r *Router) adminRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/admin").WithTagInfo(okapi.GroupTag{Name: "Admin", Description: "Platform administration: users, settings, events, metrics, jobs."})
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}

	return []okapi.RouteDefinition{
		// Users.
		{
			Method:      http.MethodGet,
			Path:        "/users",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminUser.List,
			Summary:     "List platform users",
		},
		{
			Method:      http.MethodPost,
			Path:        "/users",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminUser.Create),
			Summary:     "Create a user",
			Request:     &handlers.AdminCreateUserRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/users/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminUser.Get,
			Summary:     "Get a user with resource counts",
		},
		{
			Method:      http.MethodPut,
			Path:        "/users/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminUser.Update),
			Summary:     "Update a user",
			Request:     &handlers.AdminUpdateUserRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/users/{id}/workspace-limit",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminUser.SetWorkspaceLimit),
			Summary:     "Set/clear a user's workspace-count limit (Enterprise)",
			Request:     &handlers.AdminSetWorkspaceLimitRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/users/{id}/workspace-membership-limit",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminUser.SetWorkspaceMembershipLimit),
			Summary:     "Set/clear a user's workspace-membership limit (Enterprise)",
			Request:     &handlers.AdminSetWorkspaceMembershipLimitRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id}/schedule-deletion",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminUser.ScheduleDeletion),
			Summary:     "Schedule a disabled user for deletion after the grace period (with optional workspace ownership transfers)",
			Request:     &handlers.AdminScheduleDeletionRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id}/cancel-deletion",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminUser.CancelDeletion,
			Summary:     "Cancel a pending account deletion",
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id}/force-deletion",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminUser.ForceDeletion,
			Summary:     "Permanently delete an account that is pending deletion, skipping the grace period",
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id}/verify-email",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminUser.VerifyEmail,
			Summary:     "Mark a user's email as verified",
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id}/revoke-sessions",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminUser.RevokeSessions,
			Summary:     "Revoke a user's sessions",
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id}/disable-2fa",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminUser.DisableTwoFactor,
			Summary:     "Disable a user's two-factor auth",
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id}/reset-password",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminUser.ResetPassword,
			Summary:     "Reset a user's password (platform generates a new one)",
			Response:    &dto.Response[handlers.AdminResetPasswordResponse]{},
		},

		// Workspaces (platform admin).
		{
			Method:      http.MethodGet,
			Path:        "/workspaces",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminWorkspace.List,
			Summary:     "List all workspaces with counts",
		},
		{
			Method:      http.MethodGet,
			Path:        "/workspaces/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminWorkspace.Get,
			Summary:     "Get a workspace with members and counts",
		},
		{
			Method:      http.MethodPatch,
			Path:        "/workspaces/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminWorkspace.SetPrivileged),
			Summary:     "Set a workspace privileged flag",
			Request:     &handlers.SetWorkspacePrivilegedRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/workspaces/{id}/rotate-key",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminWorkspace.RotateKey,
			Summary:     "Rotate a workspace's encryption key (re-encrypts its secrets)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/encryption",
			Group:       g,
			Middlewares: admin,
			Handler:     handlers.NewEncryptionInfo(r.cfg.KeyAutoRotate, r.cfg.KeyRotateMonths, r.cfg.GomaConfigEncryptionKey != ""),
			Summary:     "Encryption posture (per-workspace keys, auto-rotation, gateway config encryption)",
		},

		// Domains (platform admin): list/search every workspace's domains, inspect a
		// domain with its dependent routes, and validate ownership (manual or forced).
		{
			Method:      http.MethodGet,
			Path:        "/domains",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminDomain.List,
			Summary:     "List all domains with verification status",
		},
		{
			Method:      http.MethodGet,
			Path:        "/domains/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminDomain.Get,
			Summary:     "Get a domain with its dependent routes",
		},
		{
			Method:      http.MethodPost,
			Path:        "/domains/{id}/verify",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminDomain.Verify,
			Summary:     "Validate a domain's ownership via DNS",
		},
		{
			Method:      http.MethodPost,
			Path:        "/domains/{id}/force-verify",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminDomain.ForceVerify,
			Summary:     "Force-mark a domain verified without a DNS check (override)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/domains/{id}/ban",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminDomain.Ban),
			Summary:     "Ban a domain platform-wide (forces its routes offline)",
			Request:     &handlers.BanDomainRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/domains/{id}/unban",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminDomain.Unban,
			Summary:     "Lift a domain ban",
		},

		// Routes (platform admin): list every workspace's routes with gateway sync
		// status, and resync all routes by re-rendering each workspace's config from
		// the database.
		{
			Method:      http.MethodGet,
			Path:        "/routes",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminRoute.List,
			Summary:     "List all routes with gateway sync status",
		},
		{
			Method:      http.MethodPost,
			Path:        "/routes/resync",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminRoute.Resync,
			Summary:     "Resync all routes (re-render every workspace's gateway config from the database)",
			Response:    &dto.Response[handlers.ResyncSummary]{},
		},

		// Metrics.
		{
			Method:      http.MethodGet,
			Path:        "/metrics",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminMetrics.Snapshot,
			Summary:     "Platform metrics snapshot",
			Response:    &dto.Response[handlers.PlatformMetrics]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/metrics/stream",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminMetrics.Stream,
			Summary:     "Platform metrics stream (SSE)",
		},

		// Audit log export (streamed JSON/CSV; gated audit_export → 402 in CE).
		{
			Method:      http.MethodGet,
			Path:        "/audit/export",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.auditExport.AdminExport,
			Summary:     "Export the platform audit log (JSON/CSV)",
		},

		// Events (audit feed).
		{
			Method:      http.MethodGet,
			Path:        "/events",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminEvent.List,
			Summary:     "List platform events",
		},
		{
			Method:      http.MethodGet,
			Path:        "/events/stream",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminEvent.Stream,
			Summary:     "Platform events stream (SSE)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/events/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminEvent.Get,
			Summary:     "Get a platform event",
		},

		// Update notice. Reads the cache the daily cron writes; a request never
		// calls GitHub, so refreshing the dashboard cannot burn the API budget.
		{
			Method:      http.MethodGet,
			Path:        "/update",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.update.Get,
			Summary:     "Current version and whether a newer release exists",
			Response:    &dto.Response[handlers.UpdateInfo]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/update/dismiss",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.update.Dismiss),
			Summary:     "Dismiss the update notice for a specific version",
			Request:     &handlers.DismissUpdateRequest{},
			Response:    &dto.Response[dto.MessageData]{},
		},

		// Settings.
		{
			Method:      http.MethodGet,
			Path:        "/settings",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminSetting.List,
			Summary:     "List platform settings",
		},
		{
			Method:      http.MethodPut,
			Path:        "/settings",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminSetting.Update),
			Summary:     "Update platform settings",
			Request:     &handlers.UpdateSettingsRequest{},
		},

		// Plans (per-workspace resource limits & capabilities).
		{
			Method:      http.MethodGet,
			Path:        "/plans",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlan.List,
			Summary:     "List plans",
		},
		{
			Method:      http.MethodPost,
			Path:        "/plans",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminPlan.Create),
			Summary:     "Create a plan",
			Request:     &handlers.CreatePlanRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/plans/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlan.Get,
			Summary:     "Get a plan",
		},
		{
			Method:      http.MethodPut,
			Path:        "/plans/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminPlan.Update),
			Summary:     "Update a plan",
			Request:     &handlers.UpdatePlanRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/plans/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlan.Delete,
			Summary:     "Delete a plan",
		},
		{
			Method:      http.MethodPost,
			Path:        "/plans/{id}/default",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlan.SetDefault,
			Summary:     "Set the default plan",
		},
		{
			Method:      http.MethodPut,
			Path:        "/workspaces/{workspace}/plan",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminPlan.AssignWorkspace),
			Summary:     "Assign a plan to a workspace",
			Request:     &handlers.AssignWorkspacePlanRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/workspaces/{workspace}/quota",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlan.GetWorkspaceQuota,
			Summary:     "Get a workspace's quota overrides",
		},
		{
			Method:      http.MethodPut,
			Path:        "/workspaces/{workspace}/quota",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminPlan.SetWorkspaceQuota),
			Summary:     "Set a workspace's quota overrides",
			Request:     &handlers.SetWorkspaceQuotaRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/workspaces/{workspace}/quota",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlan.DeleteWorkspaceQuota,
			Summary:     "Clear a workspace's quota overrides",
		},

		// Deployment config (platform image catalog).
		{
			Method:      http.MethodGet,
			Path:        "/deployment-config",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.deploymentCfg.Get,
			Summary:     "Get platform image catalog",
		},
		{
			Method:      http.MethodPut,
			Path:        "/deployment-config",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.deploymentCfg.Update),
			Summary:     "Update platform image catalog",
			Request:     &handlers.UpdateDeploymentConfigRequest{},
		},

		// Jobs.
		{
			Method:      http.MethodGet,
			Path:        "/jobs",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminJob.List,
			Summary:     "List scheduled jobs (paginated)",
		},
		{
			Method:      http.MethodGet,
			Path:        "/jobs/stats",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminJob.Stats,
			Summary:     "Scheduled-jobs dashboard summary",
			Response:    &dto.Response[handlers.JobStats]{},
		},

		// Platform backup (Enterprise; gated platform_backup → 402 in CE). Disaster
		// recovery for Miabi's own control-plane database and platform volumes.
		{
			Method:      http.MethodGet,
			Path:        "/platform-backup/settings",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlatformBackup.GetSettings,
			Summary:     "Get platform backup settings",
		},
		{
			Method:      http.MethodPut,
			Path:        "/platform-backup/settings",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminPlatformBackup.UpdateSettings),
			Summary:     "Update platform backup settings",
			Request:     &handlers.UpdatePlatformBackupSettingsRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/platform-backup/settings/test",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminPlatformBackup.TestSettings),
			Summary:     "Validate the platform backup S3 target",
			Request:     &handlers.UpdatePlatformBackupSettingsRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/platform-backup/backups",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlatformBackup.ListBackups,
			Summary:     "List platform backups (history)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/platform-backup/backups",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminPlatformBackup.CreateBackup),
			Summary:     "Back up the control-plane database and/or selected platform volumes now",
			Request:     &handlers.CreatePlatformBackupRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/platform-backup/backups/{id}/restore",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.adminPlatformBackup.Restore),
			Summary:     "Restore a platform backup (destructive; requires confirmation)",
			Request:     &handlers.RestorePlatformBackupRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/platform-backup/backups/{id}/download",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlatformBackup.Download,
			Summary:     "Download a local platform backup artifact",
		},
		{
			Method:      http.MethodGet,
			Path:        "/platform-backup/backups/{id}/logs/download",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlatformBackup.LogsDownload,
			Summary:     "Download a platform backup run's full logs",
		},
		{
			Method:      http.MethodDelete,
			Path:        "/platform-backup/backups/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlatformBackup.Delete,
			Summary:     "Delete a platform backup",
		},
		{
			Method:      http.MethodGet,
			Path:        "/platform-backup/volumes",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.adminPlatformBackup.Volumes,
			Summary:     "Discover candidate platform volumes",
		},

		// Commercial license (Enterprise). In Community builds Install
		// returns 402 and Get reports edition "community".
		{
			Method:      http.MethodGet,
			Path:        "/license",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.license.Get,
			Summary:     "Get the current license & entitlements",
			Response:    &dto.Response[handlers.LicenseView]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/license",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.license.Install),
			Summary:     "Install a license token",
			Request:     &handlers.InstallLicenseRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/license",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.license.Delete,
			Summary:     "Remove the license (revert to Community)",
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/license/health",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.license.Health,
			Summary:     "License warnings for the admin banner",
		},

		// OAuth providers.
		{
			Method:      http.MethodGet,
			Path:        "/oauth/providers",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.oauthAdmin.List,
			Summary:     "List OAuth providers",
		},
		{
			Method:      http.MethodPost,
			Path:        "/oauth/providers",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.oauthAdmin.Create),
			Summary:     "Create an OAuth provider",
			Request:     &handlers.CreateOAuthProviderRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/oauth/providers/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.oauthAdmin.Update),
			Summary:     "Update an OAuth provider",
			Request:     &handlers.UpdateOAuthProviderRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/oauth/providers/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.oauthAdmin.Delete,
			Summary:     "Delete an OAuth provider",
			Response:    &dto.Response[dto.MessageData]{},
		},

		// SIEM audit streaming targets (Enterprise; gated siem_stream → 402 in CE).
		{
			Method:      http.MethodGet,
			Path:        "/siem",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.siemAdmin.List,
			Summary:     "List SIEM streaming targets",
		},
		{
			Method:      http.MethodPost,
			Path:        "/siem",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.siemAdmin.Create),
			Summary:     "Create a SIEM target",
			Request:     &handlers.SIEMConfigRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/siem/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.siemAdmin.Update),
			Summary:     "Update a SIEM target",
			Request:     &handlers.SIEMConfigRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/siem/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.siemAdmin.Delete,
			Summary:     "Delete a SIEM target",
		},
		{
			Method:      http.MethodPost,
			Path:        "/siem/{id}/test",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.siemAdmin.Test,
			Summary:     "Send a synthetic event to a SIEM target",
		},
	}
}

// oauthPublicRoutes registers the unauthenticated SSO login flow.
func (r *Router) oauthPublicRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/auth/oauth").WithTagInfo(okapi.GroupTag{Name: "OAuth", Description: "Public single sign-on (SSO) login flow."})

	return []okapi.RouteDefinition{
		{
			Method:  http.MethodGet,
			Path:    "/providers",
			Group:   g,
			Handler: r.h.oauthPublic.ListProviders,
			Summary: "List enabled OAuth providers",
		},
		{
			Method:  http.MethodPost,
			Path:    "/discover",
			Group:   g,
			Handler: okapi.H(r.h.oauthPublic.DiscoverSSO),
			Summary: "Discover the SSO provider for an email domain",
			Request: &handlers.DiscoverSSORequest{},
		},
		{
			Method:  http.MethodGet,
			Path:    "/{slug}/authorize",
			Group:   g,
			Handler: r.h.oauthPublic.Authorize,
			Summary: "Begin OAuth authorization",
		},
		{
			Method:  http.MethodGet,
			Path:    "/{slug}/callback",
			Group:   g,
			Handler: r.h.oauthPublic.Callback,
			Summary: "OAuth callback",
		},
	}
}
