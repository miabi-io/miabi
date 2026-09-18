// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package middlewares

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// hasAPIKeyScope reports whether the presented key carries a scope (or the catch-all "*").
func hasAPIKeyScope(c *okapi.Context, want string) bool {
	for _, s := range APIKeyScopes(c) {
		if s == models.ScopeAll || s == want {
			return true
		}
	}
	return false
}

// RequireSystemAdmin allows only platform super-admins.
func RequireSystemAdmin(users *repositories.UserRepository) okapi.Middleware {
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
		return c.Next()
	}
}
