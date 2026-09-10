// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/handlers"
)

// capabilityRoutes serve the grantable catalogue and the granted-app inventory.
func (r *Router) capabilityRoutes() []okapi.RouteDefinition {
	tag := okapi.GroupTag{Name: "Capabilities", Description: "Grantable Linux capabilities and host devices."}
	v1 := r.v1.Group("").WithTagInfo(tag)
	sys := r.v1.Group("/system").WithTagInfo(tag)
	authed := []okapi.Middleware{r.authenticate}
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/capabilities",
			Group:       v1,
			Middlewares: authed,
			Handler:     r.h.capability.Catalog,
			Summary:     "Grantable Linux capabilities and host devices",
			Response:    &dto.Response[handlers.CapabilityCatalog]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/granted-applications",
			Group:       sys,
			Middlewares: admin,
			Handler:     r.h.capability.Inventory,
			Summary:     "Every application holding a capability or device grant (admin)",
			Response:    &dto.Response[[]handlers.GrantedApp]{},
		},
	}
}
