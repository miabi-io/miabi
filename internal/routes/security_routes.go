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
	"github.com/miabi-io/miabi/internal/services/secpolicy"
)

// securityRoutes registers the Security Center (admin) and the workspace view of its policies.
func (r *Router) securityRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/admin/security").WithTagInfo(okapi.GroupTag{Name: "Security Center", Description: "Platform security policies, audit mode and the decision log (Enterprise)."})
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}
	sensitive := []okapi.Middleware{r.authenticate, r.systemAdmin, r.freshElevation}
	// The unlock endpoints check the admin role but not the unlock itself, or nobody could unlock.
	unlock := []okapi.Middleware{r.authenticate, r.systemAdminRole}
	adm := r.v1.Group("/admin").WithTagInfo(okapi.GroupTag{Name: "Admin unlock", Description: "Second-factor unlock for the platform admin console."})
	ws := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Security Center", Description: "Platform security policies, audit mode and the decision log (Enterprise)."})

	return []okapi.RouteDefinition{
		{Method: http.MethodGet, Path: "/elevation", Group: adm, Middlewares: unlock, Handler: r.h.elevation.State,
			Response: &dto.Response[handlers.ElevationState]{}, Summary: "Whether the admin console needs an unlock, and whether this session has one"},
		{Method: http.MethodPost, Path: "/elevate", Group: adm, Middlewares: append([]okapi.Middleware{r.authRateLimit}, unlock...),
			Handler: okapi.H(r.h.elevation.Elevate), Request: &handlers.ElevateRequest{},
			Response: &dto.Response[handlers.ElevationState]{}, Summary: "Unlock the admin console with a second factor"},
		{Method: http.MethodDelete, Path: "/elevation", Group: adm, Middlewares: unlock, Handler: r.h.elevation.Lock,
			Summary: "Lock the admin console again"},
		{Method: http.MethodGet, Path: "/status", Group: g, Middlewares: admin, Handler: r.h.security.Status,
			Summary: "Security Center entitlement and kill-switch state"},
		{Method: http.MethodGet, Path: "/overview", Group: g, Middlewares: admin, Handler: r.h.security.Overview,
			Summary: "Security posture and the last 24 hours of decisions"},
		{Method: http.MethodGet, Path: "/policies", Group: g, Middlewares: admin, Handler: r.h.security.ListPolicies,
			Summary: "List security policies"},
		{Method: http.MethodPut, Path: "/policies", Group: g, Middlewares: sensitive, Handler: okapi.H(r.h.security.SavePolicy),
			Request: &handlers.SavePolicyRequest{}, Summary: "Create or replace the policy for a kind at a scope (Enterprise)"},
		{Method: http.MethodDelete, Path: "/policies/{policyID}", Group: g, Middlewares: sensitive, Handler: r.h.security.DeletePolicy,
			Summary: "Delete a security policy"},
		{Method: http.MethodPost, Path: "/ports/revoke", Group: g, Middlewares: admin, Handler: okapi.H(r.h.security.RevokePorts),
			Request: &handlers.RevokePortsRequest{}, Summary: "Revoke every approved host port (Enterprise; dry_run lists them)"},
		{Method: http.MethodGet, Path: "/events", Group: g, Middlewares: admin, Handler: r.h.security.Events,
			Summary: "List policy decisions"},
		{Method: http.MethodGet, Path: "/events/export", Group: g, Middlewares: admin, Handler: r.h.security.ExportEvents,
			Summary: "Export policy decisions as CSV"},
		{Method: http.MethodGet, Path: "/{workspace}/port-policy", Group: ws,
			Middlewares: []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(models.WorkspaceRoleViewer)},
			Handler:     r.h.security.WorkspacePortsPolicy, Response: &dto.Response[secpolicy.PortsPolicyView]{},
			Summary: "Whether this workspace may request host ports"},
	}
}
