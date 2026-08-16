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

func (r *Router) networkRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Networks", Description: "Managed Docker networks."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/networks"
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.network.List,
			Summary:     "List networks",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.network.Create),
			Summary:     "Create a network",
			Request:     &handlers.CreateNetworkRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{networkID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.network.Delete,
			Summary:     "Delete a network",
		},
	}
}

func (r *Router) routeRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Routes", Description: "Goma Gateway routes."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/routes"
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.route.List,
			Summary:     "List routes",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.route.Create),
			Summary:     "Create a route",
			Request:     &handlers.CreateRouteRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{routeID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.route.Get,
			Summary:     "Get a route",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{routeID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.route.Update),
			Summary:     "Update a route",
			Request:     &handlers.UpdateRouteRequest{},
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{routeID}/enabled",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.route.SetEnabled),
			Summary:     "Enable or disable a route",
			Request:     &handlers.SetRouteEnabledRequest{},
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{routeID}/security",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.route.SetSecurity),
			Summary:     "Set a route's gateway security switches",
			Request:     &handlers.SetRouteSecurityRequest{},
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{routeID}/maintenance",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.route.SetMaintenance),
			Summary:     "Put a route into or out of maintenance mode",
			Request:     &handlers.SetRouteMaintenanceRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{routeID}/middlewares",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.route.AttachMiddleware),
			Summary:     "Attach a middleware to a route",
			Request:     &handlers.AttachRouteMiddlewareRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{routeID}/middlewares/{name}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.route.DetachMiddleware,
			Summary:     "Detach a middleware from a route",
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{routeID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.route.Delete,
			Summary:     "Delete a route",
		},
	}
}

func (r *Router) domainRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Domains", Description: "Owned domains: DNS-verified hostnames with a default TLS policy."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/domains"
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.domain.List,
			Summary:     "List domains",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.domain.Create),
			Summary:     "Register a domain",
			Request:     &handlers.CreateDomainRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{domainID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.domain.Get,
			Summary:     "Get a domain",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{domainID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.domain.Update),
			Summary:     "Update a domain",
			Request:     &handlers.UpdateDomainRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{domainID}/verify",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.domain.Verify,
			Summary:     "Verify domain ownership via DNS",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{domainID}/dns-provider",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.domain.SetDNSProvider),
			Summary:     "Link or unlink a DNS provider for automated DNS",
			Request:     &handlers.SetDNSProviderRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{domainID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.domain.Delete,
			Summary:     "Delete a domain",
		},
	}
}

// dnsProviderRoutes registers workspace DNS provider connections; credentials are write-only.
func (r *Router) dnsProviderRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "DNS Providers", Description: "Connected DNS hosts for automated ownership verification and app records."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/dns/providers"
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.dnsProvider.List,
			Summary:     "List DNS providers",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.dnsProvider.Connect),
			Summary:     "Connect a DNS provider",
			Request:     &handlers.ConnectDNSProviderRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{providerID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.dnsProvider.Get,
			Summary:     "Get a DNS provider",
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/{providerID}/test",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.dnsProvider.Test),
			Summary:     "Test a DNS provider connection against a zone",
			Request:     &handlers.TestDNSProviderRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{providerID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.dnsProvider.Delete,
			Summary:     "Disconnect a DNS provider",
		},
	}
}

// portBindingRoutes registers host port binding requests plus the platform-admin review endpoints.
func (r *Router) portBindingRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Port Bindings", Description: "Host port bindings (admin-validated)."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}
	const base = "/{workspace}/port-bindings"

	sys := r.v1.Group("/system").WithTagInfo(okapi.GroupTag{Name: "Port Bindings", Description: "Host port bindings (admin-validated)."})

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.portBinding.List,
			Summary:     "List port bindings",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/suggest",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.portBinding.Suggest,
			Summary:     "Suggest a free host port on the app's node",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.portBinding.Request),
			Summary:     "Request a host port binding",
			Request:     &handlers.RequestPortBindingRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{bindingID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.portBinding.Cancel,
			Summary:     "Cancel a port binding",
		},

		{
			Method:      http.MethodGet,
			Path:        "/port-bindings",
			Group:       sys,
			Middlewares: admin,
			Handler:     r.h.portBinding.AdminList,
			Summary:     "List port bindings for review (admin)",
		},
		{
			Method:      http.MethodPost,
			Path:        "/port-bindings/{bindingID}/approve",
			Group:       sys,
			Middlewares: admin,
			Handler:     okapi.H(r.h.portBinding.Approve),
			Summary:     "Approve a port binding (admin)",
			Request:     &handlers.ReviewPortBindingRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/port-bindings/{bindingID}/reject",
			Group:       sys,
			Middlewares: admin,
			Handler:     okapi.H(r.h.portBinding.Reject),
			Summary:     "Reject a port binding (admin)",
			Request:     &handlers.ReviewPortBindingRequest{},
		},
	}
}

func (r *Router) middlewareRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Middlewares", Description: "Goma Gateway middlewares."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/middlewares"
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/middleware-catalog",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.middleware.Catalog,
			Summary:     "List the curated middleware-type catalog (security policies)",
		},
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.middleware.List,
			Summary:     "List middlewares",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.middleware.Create),
			Summary:     "Create a middleware",
			Request:     &handlers.CreateMiddlewareRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{middlewareID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.middleware.Get,
			Summary:     "Get a middleware",
		},
		{
			Method:      http.MethodPatch,
			Path:        base + "/{middlewareID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.middleware.Update),
			Summary:     "Update a middleware",
			Request:     &handlers.UpdateMiddlewareRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{middlewareID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.middleware.Delete,
			Summary:     "Delete a middleware",
		},
	}
}
