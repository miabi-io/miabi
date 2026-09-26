// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package middlewares

import (
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/elevation"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

const ctxElevation = "admin_elevation"

// hasAPIKeyScope reports whether the presented key carries a scope (or the catch-all "*").
func hasAPIKeyScope(c *okapi.Context, want string) bool {
	for _, s := range APIKeyScopes(c) {
		if s == models.ScopeAll || s == want {
			return true
		}
	}
	return false
}

// RequireSystemAdmin allows only platform super-admins. With a gate, a session caller also needs a
// live console unlock (API keys are a separate credential and skip it), and an Enterprise
// allowlist applies to every caller. A missing unlock is a 403 with ADMIN_ELEVATION_REQUIRED,
// never a 401, which the web client would read as "signed out".
func RequireSystemAdmin(users *repositories.UserRepository, gate *elevation.Service) okapi.Middleware {
	return func(c *okapi.Context) error {
		uid := UserID(c)
		if uid == 0 {
			return c.AbortUnauthorized("authentication required")
		}

		if AuthMethod(c) == "api_key" {
			if APIKeyEphemeral(c) || APIKeyAppID(c) != 0 || APIKeyWorkspaceID(c) != nil {
				return c.AbortForbidden("platform admin endpoints need an account-wide API key, not a scoped one")
			}
			if !hasAPIKeyScope(c, models.ScopeAdmin) {
				return c.AbortForbidden("platform admin endpoints need an API key with the admin scope")
			}
		}
		user, err := users.FindByID(uid)
		if err != nil || user.Role != models.SystemRoleAdmin {
			return c.AbortForbidden("platform admin required")
		}
		if gate != nil {
			if err := checkElevation(c, gate, user); err != nil {
				return c.AbortForbidden(err.Error(), err)
			}
		}
		return c.Next()
	}
}

func checkElevation(c *okapi.Context, gate *elevation.Service, user *models.User) error {
	set := gate.Settings()
	if !elevation.IPAllowed(set, c.RealIP()) {
		return elevation.ErrIPNotAllowed
	}
	if !set.Required || AuthMethod(c) == "api_key" {
		return nil
	}
	if !user.TwoFactorEnabled {
		return elevation.ErrSetupRequired
	}
	rec, err := gate.Check(c.Request().Context(), user.ID, SessionJTI(c), UnlockToken(c))
	if err != nil {
		return err
	}
	c.Set(ctxElevation, rec)
	return nil
}

// RequireFreshElevation guards a sensitive admin action (licence, security policies, deleting an
// organization): the unlock must be younger than the sensitive window, even inside its TTL. Runs
// after RequireSystemAdmin.
func RequireFreshElevation(gate *elevation.Service) okapi.Middleware {
	return func(c *okapi.Context) error {
		if gate == nil || AuthMethod(c) == "api_key" || !gate.Settings().Required {
			return c.Next()
		}
		rec, ok := c.Get(ctxElevation)
		r, isRec := rec.(elevation.Record)
		if !ok || !isRec || !gate.Fresh(r) {
			return c.AbortForbidden(elevation.ErrStale.Error(), elevation.ErrStale)
		}
		return c.Next()
	}
}

// FreshUnlock returns nil when the caller holds an unlock younger than the sensitive window, or
// the unlock is not in force; otherwise the coded error to answer with. For sensitive actions that
// live outside the admin routes, such as minting an admin-scope API key.
func FreshUnlock(c *okapi.Context, gate *elevation.Service) error {
	if gate == nil || AuthMethod(c) == "api_key" || !gate.Settings().Required {
		return nil
	}
	rec, err := gate.Peek(c.Request().Context(), UserID(c), SessionJTI(c), UnlockToken(c))
	if err != nil {
		return elevation.ErrRequired
	}
	if !gate.Fresh(rec) {
		return elevation.ErrStale
	}
	return nil
}

// SessionJTI returns the session token's jti, which an unlock is bound to.
func SessionJTI(c *okapi.Context) string { return c.GetString(ctxJTI) }

// SessionExpiry returns when the session token expires, or zero when unknown.
func SessionExpiry(c *okapi.Context) time.Time {
	v, ok := c.Get("jwt_user")
	if !ok {
		return time.Time{}
	}
	claims, ok := v.(jwt.MapClaims)
	if !ok {
		return time.Time{}
	}
	if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
		return exp.Time
	}
	return time.Time{}
}

// UnlockToken reads the console unlock from the header (CLI) or the cookie (browser).
func UnlockToken(c *okapi.Context) string {
	if h := strings.TrimSpace(c.Header(elevation.HeaderName)); h != "" {
		return h
	}
	if ck, err := c.Cookie(elevation.CookieName); err == nil {
		return strings.TrimSpace(ck)
	}
	return ""
}
