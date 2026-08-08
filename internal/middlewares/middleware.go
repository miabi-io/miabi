// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package middlewares holds HTTP middleware: authentication, workspace scoping,
// and role-based access control.
package middlewares

import (
	"errors"
	"net"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/session"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Context keys populated by these middlewares.
const (
	CtxUserID        = "user_id"
	CtxAuthMethod    = "auth_method"
	CtxWorkspaceID   = "workspace_id"
	CtxWorkspaceRole = "workspace_role"
	// CtxPermissions holds the caller's effective permission set
	// (map[models.Permission]bool), resolved by WorkspaceScope.
	CtxPermissions  = "workspace_permissions"
	CtxAPIKeyID     = "api_key_id"
	CtxAPIKeyScopes = "api_key_scopes"
	// CtxAPIKeyWorkspaceID holds the workspace an API key is bound to, when the presented key is
	// workspace-scoped (nil for account-wide keys). It lets /me report which workspace a token
	// manages, so machine clients need only supply the token.
	CtxAPIKeyWorkspaceID = "api_key_workspace_id"
	ctxJTI               = "jti"
)

// SessionCookieName holds the user JWT for browser clients. It is HttpOnly (so
// XSS can't read it) and SameSite=Strict (so it isn't sent cross-site, blocking
// CSRF); non-browser clients (CLI, API) keep using the Authorization header.
const SessionCookieName = "miabi_session"

func baseJWT(cfg *config.Config, store *session.Store) okapi.JWTAuth {
	a := okapi.JWTAuth{
		SigningSecret: []byte(cfg.JWTSecret),
		Audience:      "miabi",
		ContextKey:    "jwt_user",
		// Header (CLI/API) first, then the browser session cookie. The JWT is NOT accepted via ?token= — browsers
		// send the cookie on SSE/WebSocket handshakes and CLIs set the Authorization header — so a session token
		// never lands in a URL (and thus proxy/access logs or browser history).
		TokenLookup: "header:Authorization,cookie:" + SessionCookieName,
		ForwardClaims: map[string]string{
			CtxUserID: "sub",
			"email":   "email",
			"role":    "role",
			ctxJTI:    "jti",
		},
	}
	a.OnUnauthorized = func(c *okapi.Context) error {
		return c.AbortUnauthorized("invalid or expired session")
	}
	if store != nil {
		a.ValidateClaims = func(c *okapi.Context, claims jwt.Claims) error {
			mc, ok := claims.(jwt.MapClaims)
			if !ok {
				return nil
			}
			jti, _ := mc["jti"].(string)
			if jti == "" {
				return errors.New("invalid token: missing jti")
			}
			if store.IsRevoked(c.Request().Context(), jti) {
				return errors.New("session has been revoked")
			}
			return nil
		}
	}
	return a
}

// JWTAuth builds user JWT auth (Authorization header or session cookie, per
// baseJWT's TokenLookup). Revoked sessions are rejected.
func JWTAuth(cfg *config.Config, store *session.Store) okapi.JWTAuth {
	return baseJWT(cfg, store)
}

// Authenticate accepts a user JWT or an API key. API keys (mb_...) are read from the Authorization
// header or the ?token= query param; any other credential is treated as a JWT and resolved from
// the header or session cookie per JWTAuth.TokenLookup — a JWT is never accepted via ?token=.
func Authenticate(jwtAuth okapi.JWTAuth, apiKeys *auth.APIKeyService, users *repositories.UserRepository) okapi.Middleware {
	return func(c *okapi.Context) error {
		raw := bearerToken(c)
		if raw == "" {
			raw = strings.TrimSpace(c.Query("token"))
		}
		if raw == "" {
			// Browser clients present the JWT as an HttpOnly cookie (no header/query);
			// surface it so the empty-credential guard below passes. Okapi's JWT
			// middleware re-reads it from the cookie via TokenLookup.
			if ck, err := c.Cookie(SessionCookieName); err == nil {
				raw = strings.TrimSpace(ck)
			}
		}
		if strings.HasPrefix(raw, "mb_") {
			return authAPIKey(c, apiKeys, users, raw)
		}
		if raw == "" {
			return c.AbortUnauthorized("authentication required")
		}
		c.Set(CtxAuthMethod, "jwt")
		return jwtAuth.Middleware(c)
	}
}

// bearerToken returns the token from the Authorization header, stripping an
// optional `Bearer ` prefix (so a bare `Authorization: mb_…` key still works).
func bearerToken(c *okapi.Context) string {
	return strings.TrimSpace(strings.TrimPrefix(c.Header("Authorization"), "Bearer "))
}

func authAPIKey(c *okapi.Context, apiKeys *auth.APIKeyService, users *repositories.UserRepository, raw string) error {
	key, err := apiKeys.Verify(raw)
	if err != nil {
		return c.AbortUnauthorized("invalid API key")
	}
	user, err := users.FindByID(key.UserID)
	if err != nil {
		return c.AbortUnauthorized("invalid API key")
	}
	if !user.Active {
		return c.AbortForbidden("account is disabled")
	}
	if !ipAllowed(key.AllowedIPs, c.RealIP()) {
		return c.AbortForbidden("IP address not allowed for this API key")
	}
	// A registry-only key (only registry_* scopes) is for docker login/push/pull,
	// which the gateway authorizes at /internal/registry/auth — it has no business
	// on the general API, so refuse it here.
	if key.IsRegistryOnly() {
		return c.AbortForbidden("this API key is limited to the container registry")
	}

	scopes := key.Scopes
	if len(scopes) == 0 {
		scopes = []string{models.ScopeRead}
	}
	c.Set(CtxUserID, int(key.UserID))
	c.Set(CtxAuthMethod, "api_key")
	c.Set(CtxAPIKeyID, int(key.ID))
	c.Set(CtxAPIKeyScopes, strings.Join(scopes, ","))
	// A workspace-bound key carries its workspace; expose it so /me can report it.
	// (Purely additive: tenant resolution still happens in WorkspaceScope.)
	if key.WorkspaceID != nil {
		c.Set(CtxAPIKeyWorkspaceID, int(*key.WorkspaceID))
	}
	return c.Next()
}

// ipAllowed reports whether clientIP is permitted by an API key's allowlist. An
// empty allowlist permits any IP; entries may be exact IPs or CIDR ranges.
func ipAllowed(allowed []string, clientIP string) bool {
	if len(allowed) == 0 {
		return true
	}
	ip := net.ParseIP(clientIP)
	for _, a := range allowed {
		if a == clientIP {
			return true
		}
		if strings.Contains(a, "/") {
			if _, network, err := net.ParseCIDR(a); err == nil && ip != nil && network.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// RequireScope guards a route for API-key callers, demanding the presented key
// carry the given scope (or "*"). JWT/session callers are unaffected — their
// access is governed by the workspace RBAC middleware instead.
func RequireScope(scope string) okapi.Middleware {
	return func(c *okapi.Context) error {
		if c.GetString(CtxAuthMethod) != "api_key" {
			return c.Next()
		}
		for _, s := range strings.Split(c.GetString(CtxAPIKeyScopes), ",") {
			if s == models.ScopeAll || s == scope {
				return c.Next()
			}
		}
		return c.AbortForbidden("API key missing required scope: " + scope)
	}
}

// UserID returns the authenticated user id (0 if absent).
func UserID(c *okapi.Context) uint {
	return uint(c.GetInt(CtxUserID))
}

// AuthMethod returns how the request authenticated ("jwt" | "api_key" | "").
func AuthMethod(c *okapi.Context) string { return c.GetString(CtxAuthMethod) }

// APIKeyID returns the presented API key's id (0 for session/JWT callers).
func APIKeyID(c *okapi.Context) uint { return uint(c.GetInt(CtxAPIKeyID)) }

// APIKeyWorkspaceID returns the workspace an API key is bound to, or nil for an
// account-wide key or a non-API-key caller.
func APIKeyWorkspaceID(c *okapi.Context) *uint {
	if id := c.GetInt(CtxAPIKeyWorkspaceID); id > 0 {
		v := uint(id)
		return &v
	}
	return nil
}

// APIKeyScopes returns the presented key's scopes (nil for session/JWT callers).
func APIKeyScopes(c *okapi.Context) []string {
	raw := c.GetString(CtxAPIKeyScopes)
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}
