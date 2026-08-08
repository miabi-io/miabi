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

func (r *Router) certificateRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Certificates", Description: "Imported TLS certificates (bring-your-own; ACME is handled by Goma)."})
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}
	const base = "/{workspace}/certificates"

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.certificate.List,
			Summary:     "List certificates (metadata; ?host= filters by SAN match)",
		},
		{
			Method:      http.MethodPost,
			Path:        base,
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.certificate.Import),
			Summary:     "Import a certificate (parsed + validated)",
			Request:     &handlers.ImportCertificateRequest{},
		},
		{
			Method:      http.MethodPost,
			Path:        base + "/issue",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.certificate.Issue),
			Summary:     "Issue a managed certificate via ACME DNS-01",
			Request:     &handlers.IssueCertificateRequest{},
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{certID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.certificate.Get,
			Summary:     "Get a certificate (metadata)",
		},
		{
			Method:      http.MethodGet,
			Path:        base + "/{certID}/usage",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.certificate.Usage,
			Summary:     "List routes referencing a certificate",
		},
		{
			Method:      http.MethodPut,
			Path:        base + "/{certID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     okapi.H(r.h.certificate.Replace),
			Summary:     "Replace a certificate (renewal)",
			Request:     &handlers.ReplaceCertificateRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        base + "/{certID}",
			Group:       g,
			Middlewares: scoped(models.WorkspaceRoleDeveloper),
			Handler:     r.h.certificate.Delete,
			Summary:     "Delete a certificate",
		},
	}
}
