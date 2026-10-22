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

// Package middlewares holds HTTP middleware: authentication, workspace scoping,
// and role-based access control.
package middlewares

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

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
	CtxUserID            = "user_id"
	CtxAuthMethod        = "auth_method"
	CtxWorkspaceID       = "workspace_id"
	CtxAppID             = "app_id"
	CtxWorkspaceRole     = "workspace_role"
	CtxPermissions       = "workspace_permissions"
	CtxAPIKeyID          = "api_key_id"
	CtxAPIKeyScopes      = "api_key_scopes"
	CtxAPIKeyWorkspaceID = "api_key_workspace_id"
	CtxAPIKeyEphemeral   = "api_key_ephemeral"
	CtxAPIKeyAppID       = "api_key_app_id"
	// CtxSuspended holds a suspended caller's suspension reason.
	CtxSuspended = "user_suspended"
	ctxJTI       = "jti"
)

// SessionCookieName holds the user JWT for browser clients. It is HttpOnly (so
// XSS can't read it) and SameSite=Strict (so it isn't sent cross-site, blocking
// CSRF); non-browser clients (CLI, API) keep using the Authorization header.
const SessionCookieName = "miabi_session"

func baseJWT(cfg *config.Config, store *session.Store, users *repositories.UserRepository) okapi.JWTAuth {
	a := okapi.JWTAuth{
		SigningSecret: []byte(cfg.JWTSecret),
		Audience:      "miabi",
		ContextKey:    "jwt_user",
		TokenLookup:   "header:Authorization,cookie:" + SessionCookieName,
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
	key := []byte(cfg.JWTSecret)
	a.ValidateClaims = func(c *okapi.Context, claims jwt.Claims) error {
		mc, ok := claims.(jwt.MapClaims)
		if !ok {
			return nil
		}
		if store != nil {
			jti, _ := mc["jti"].(string)
			if jti == "" {
				return errors.New("invalid token: missing jti")
			}
			if store.IsRevoked(c.Request().Context(), jti) {
				return errors.New("session has been revoked")
			}
		}
		if users != nil {
			user, err := checkSessionUser(users, key, mc)
			if err != nil {
				return err
			}
			markSuspended(c, user)
		}
		return nil
	}
	return a
}

// checkSessionUser ends a session whose user is gone, disabled, or has changed password since it was
// issued: a token alone outlives all three for up to its 24 h lifetime. API keys reload the user too.
func checkSessionUser(users *repositories.UserRepository, key []byte, mc jwt.MapClaims) (*models.User, error) {
	sub, ok := mc["sub"].(float64)
	if !ok || sub <= 0 {
		return nil, errors.New("invalid token: missing subject")
	}
	user, err := users.FindByID(uint(sub))
	if err != nil {
		return nil, errors.New("session is no longer valid")
	}
	if !user.Active {
		return nil, errors.New("account is disabled")
	}
	// A token minted before the fingerprint existed carries none; it still expires within TokenTTL.
	if pwf, has := mc[auth.ClaimPasswordFingerprint].(string); has && pwf != auth.SessionFingerprint(key, user.PasswordHash) {
		return nil, errors.New("session ended by a password change")
	}
	return user, nil
}

// markSuspended records a suspended caller for SuspensionGuard, which refuses their writes later in
// the chain: refusing here would end the session, and a suspended user keeps read access.
func markSuspended(c *okapi.Context, u *models.User) {
	if u != nil && u.Suspended(time.Now()) {
		reason := u.SuspendedReason
		if reason == "" {
			reason = "no reason given"
		}
		c.Set(CtxSuspended, reason)
	}
}

// UserSuspendedError refuses a write by a suspended user.
type UserSuspendedError struct{ Reason string }

func (e *UserSuspendedError) Error() string {
	return "your account is suspended by the platform administrator: " + e.Reason
}

// Code is the stable machine code surfaced in the API error envelope.
func (e *UserSuspendedError) Code() string { return "USER_SUSPENDED" }

// SuspensionGuard refuses writes by a suspended user. Sign-in and sign-out flows under /auth/ stay
// open. It is a no-op for anyone not suspended, so it is safe on every route.
func SuspensionGuard() okapi.Middleware {
	return func(c *okapi.Context) error {
		reason := c.GetString(CtxSuspended)
		if reason == "" || safeMethod(c.Request().Method) || strings.Contains(c.Request().URL.Path, "/auth/") {
			return c.Next()
		}
		return c.AbortWithError(http.StatusForbidden, &UserSuspendedError{Reason: reason})
	}
}

// JWTAuth builds user JWT auth (Authorization header or session cookie, per
// baseJWT's TokenLookup). Revoked sessions are rejected.
func JWTAuth(cfg *config.Config, store *session.Store, users *repositories.UserRepository) okapi.JWTAuth {
	return baseJWT(cfg, store, users)
}

// Authenticate accepts a user JWT or an API key. API keys (mb_...) are read from the Authorization
// header or the ?token= query param; any other credential is treated as a JWT and resolved from
// the header or session cookie per JWTAuth.TokenLookup — a JWT is never accepted via ?token=.
func Authenticate(jwtAuth okapi.JWTAuth, apiKeys *auth.APIKeyService, users *repositories.UserRepository, apps AppIDResolver) okapi.Middleware {
	return func(c *okapi.Context) error {
		raw := bearerToken(c)
		if raw == "" {
			raw = strings.TrimSpace(c.Query("token"))
		}
		if raw == "" {

			if ck, err := c.Cookie(SessionCookieName); err == nil {
				raw = strings.TrimSpace(ck)
			}
		}
		if strings.HasPrefix(raw, "mb_") {
			return authAPIKey(c, apiKeys, users, apps, raw)
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

func authAPIKey(c *okapi.Context, apiKeys *auth.APIKeyService, users *repositories.UserRepository, apps AppIDResolver, raw string) error {
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
	markSuspended(c, user)
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

	if key.WorkspaceID != nil {
		c.Set(CtxAPIKeyWorkspaceID, int(*key.WorkspaceID))
	}
	var boundApp uint
	if key.ApplicationID != nil {
		boundApp = *key.ApplicationID
		c.Set(CtxAPIKeyAppID, int(boundApp))
	}
	// A machine-minted job credential is confined to the application it was issued for.
	if key.Ephemeral {
		c.Set(CtxAPIKeyEphemeral, true)
		var keyWS uint
		if key.WorkspaceID != nil {
			keyWS = *key.WorkspaceID
		}
		if err := ephemeralKeyRefusal(c, boundApp, apps, keyWS); err != nil {
			return c.AbortForbidden(err.Error())
		}
	}

	if key.WorkspaceID != nil {
		if err := workspaceKeyRefusal(c); err != nil {
			return c.AbortForbidden(err.Error())
		}
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

// APIKeyEphemeral reports whether the caller presented a machine-minted job credential.
func APIKeyEphemeral(c *okapi.Context) bool { return c.GetBool(CtxAPIKeyEphemeral) }

// APIKeyAppID returns the application an API key is bound to, or 0 when it is not app-bound.
func APIKeyAppID(c *okapi.Context) uint { return uint(c.GetInt(CtxAPIKeyAppID)) }

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
