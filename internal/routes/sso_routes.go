// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/handlers"
)

// ssoAdminRoutes registers the identity-realm admin endpoints: the organization
// enforced-SSO toggle and SAML connection CRUD. SAML routes are gated on the
// sso_saml entitlement inside the handler (402 when unlicensed).
func (r *Router) ssoAdminRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/admin").WithTagInfo(okapi.GroupTag{Name: "SSO", Description: "Identity realm: enforced SSO and SAML 2.0 connections."})
	admin := []okapi.Middleware{r.authenticate, r.systemAdmin}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/organization",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.ssoAdmin.GetOrganization,
			Summary:     "Get the default organization",
		},
		{
			Method:      http.MethodPut,
			Path:        "/organization",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.ssoAdmin.UpdateOrganization),
			Summary:     "Update the organization (enforced SSO)",
			Request:     &handlers.UpdateOrganizationRequest{},
		},

		{
			Method:      http.MethodGet,
			Path:        "/sso/saml",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.ssoAdmin.ListSAML,
			Summary:     "List SAML connections",
		},
		{
			Method:      http.MethodPost,
			Path:        "/sso/saml",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.ssoAdmin.CreateSAML),
			Summary:     "Create a SAML connection",
			Request:     &handlers.SAMLConfigRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/sso/saml/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.ssoAdmin.UpdateSAML),
			Summary:     "Update a SAML connection",
			Request:     &handlers.SAMLConfigRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/sso/saml/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.ssoAdmin.DeleteSAML,
			Summary:     "Delete a SAML connection",
		},

		{
			Method:      http.MethodGet,
			Path:        "/sso/ldap",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.ldapAdmin.List,
			Summary:     "List LDAP/AD connections",
		},
		{
			Method:      http.MethodPost,
			Path:        "/sso/ldap",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.ldapAdmin.Create),
			Summary:     "Create an LDAP/AD connection",
			Request:     &handlers.LDAPConfigRequest{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/sso/ldap/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.ldapAdmin.Update),
			Summary:     "Update an LDAP/AD connection",
			Request:     &handlers.LDAPConfigRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/sso/ldap/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.ldapAdmin.Delete,
			Summary:     "Delete an LDAP/AD connection",
		},
		{
			Method:      http.MethodPost,
			Path:        "/sso/ldap/{id}/test",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.ldapAdmin.TestConnection,
			Summary:     "Test an LDAP/AD connection",
		},
		{
			Method:      http.MethodPost,
			Path:        "/sso/ldap/{id}/mappings",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.ldapAdmin.CreateMapping),
			Summary:     "Add an LDAP group→access mapping",
			Request:     &handlers.LDAPMappingRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/sso/ldap/{id}/mappings/{mappingID}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.ldapAdmin.DeleteMapping,
			Summary:     "Delete an LDAP group→access mapping",
		},

		{
			Method:      http.MethodGet,
			Path:        "/scim/tokens",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.ssoAdmin.ListSCIMTokens,
			Summary:     "List SCIM tokens",
		},
		{
			Method:      http.MethodPost,
			Path:        "/scim/tokens",
			Group:       g,
			Middlewares: admin,
			Handler:     okapi.H(r.h.ssoAdmin.CreateSCIMToken),
			Summary:     "Mint a SCIM token",
			Request:     &handlers.CreateSCIMTokenRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/scim/tokens/{id}",
			Group:       g,
			Middlewares: admin,
			Handler:     r.h.ssoAdmin.DeleteSCIMToken,
			Summary:     "Revoke a SCIM token",
		},
	}
}

// scimRoutes registers the SCIM 2.0 provisioning endpoints at /scim/v2 (the
// well-known SCIM base, outside /api/v1). Auth is the SCIM bearer token, checked
// inside the handler. The seam is nil in Community → 402.
func (r *Router) scimRoutes() []okapi.RouteDefinition {
	g := r.app.Group("/scim/v2").WithTagInfo(okapi.GroupTag{Name: "SCIM", Description: "SCIM 2.0 user provisioning (Enterprise)."})
	return []okapi.RouteDefinition{
		{
			Method:  http.MethodGet,
			Path:    "/Users",
			Group:   g,
			Handler: r.scimUsers,
			Summary: "List/search users",
		},
		{
			Method:  http.MethodPost,
			Path:    "/Users",
			Group:   g,
			Handler: r.scimUsers,
			Summary: "Provision a user",
		},
		{
			Method:  http.MethodGet,
			Path:    "/Users/{id}",
			Group:   g,
			Handler: r.scimUsers,
			Summary: "Get a user",
		},
		{
			Method:  http.MethodPut,
			Path:    "/Users/{id}",
			Group:   g,
			Handler: r.scimUsers,
			Summary: "Replace a user",
		},
		{
			Method:  http.MethodPatch,
			Path:    "/Users/{id}",
			Group:   g,
			Handler: r.scimUsers,
			Summary: "Patch a user (deprovision)",
		},
		{
			Method:  http.MethodDelete,
			Path:    "/Users/{id}",
			Group:   g,
			Handler: r.scimUsers,
			Summary: "Deprovision a user",
		},
		{
			Method:  http.MethodGet,
			Path:    "/Groups",
			Group:   g,
			Handler: r.scimGroups,
			Summary: "List groups",
		},
		{
			Method:  http.MethodGet,
			Path:    "/Groups/{id}",
			Group:   g,
			Handler: r.scimGroups,
			Summary: "Get a group",
		},
	}
}

func (r *Router) scimProvider() enterprise.SCIMProvider {
	if r.ee == nil {
		return nil
	}
	return r.ee.SCIM()
}

func (r *Router) scimUsers(c *okapi.Context) error {
	sp := r.scimProvider()
	if sp == nil {
		return c.AbortWithError(402, enterprise.ErrLicenseRequired)
	}
	return sp.Users(c)
}

func (r *Router) scimGroups(c *okapi.Context) error {
	sp := r.scimProvider()
	if sp == nil {
		return c.AbortWithError(402, enterprise.ErrLicenseRequired)
	}
	return sp.Groups(c)
}

// samlPublicRoutes registers the unauthenticated SAML SP endpoints. They exist in
// every build; in Community (or when sso_saml is not entitled) the seam handler is
// nil and the wrappers return 402.
func (r *Router) samlPublicRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/auth/saml").WithTagInfo(okapi.GroupTag{Name: "SAML", Description: "SAML 2.0 service-provider login flow (Enterprise)."})
	return []okapi.RouteDefinition{
		{
			Method:  http.MethodGet,
			Path:    "/{slug}/metadata",
			Group:   g,
			Handler: r.samlMetadata,
			Summary: "SP metadata XML",
		},
		{
			Method:  http.MethodGet,
			Path:    "/{slug}/login",
			Group:   g,
			Handler: r.samlLogin,
			Summary: "Begin SAML SSO",
		},
		{
			Method:  http.MethodPost,
			Path:    "/{slug}/acs",
			Group:   g,
			Handler: r.samlACS,
			Summary: "SAML assertion consumer service",
		},
	}
}

func (r *Router) samlProvider() enterprise.SAMLProvider {
	if r.ee == nil {
		return nil
	}
	return r.ee.SAML()
}

func (r *Router) samlMetadata(c *okapi.Context) error {
	sp := r.samlProvider()
	if sp == nil {
		return c.AbortWithError(402, enterprise.ErrLicenseRequired)
	}
	return sp.Metadata(c)
}

func (r *Router) samlLogin(c *okapi.Context) error {
	sp := r.samlProvider()
	if sp == nil {
		return c.AbortWithError(402, enterprise.ErrLicenseRequired)
	}
	return sp.Login(c)
}

func (r *Router) samlACS(c *okapi.Context) error {
	sp := r.samlProvider()
	if sp == nil {
		return c.AbortWithError(402, enterprise.ErrLicenseRequired)
	}
	return sp.ACS(c)
}
