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

package middlewares

import (
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
)

// Warn exists for upgraded installs whose default read-scope keys may have been writing for months.
type ScopeMode string

const (
	// ScopeModeOff skips the check entirely — the behaviour before scopes were enforced.
	ScopeModeOff  ScopeMode = "off"
	ScopeModeWarn ScopeMode = "warn"
	// ScopeModeEnforce refuses a request whose key lacks the scope.
	ScopeModeEnforce ScopeMode = "enforce"
)

// Lets a client see the coming break in its own responses, not only the operator's audit log.
const ScopeViolationHeader = "X-Miabi-Scope-Required"

// ParseScopeMode falls back to warn for anything unrecognised: a typo must not disable a security control.
func ParseScopeMode(s string) ScopeMode {
	switch ScopeMode(strings.ToLower(strings.TrimSpace(s))) {
	case ScopeModeOff:
		return ScopeModeOff
	case ScopeModeEnforce:
		return ScopeModeEnforce
	default:
		return ScopeModeWarn
	}
}

// Upgraded keys predate enforcement and may rely on scopes they never had.
const LegacyScopeModeSetting = "api_key_scope_enforcement_legacy"

// ResolveScopeMode prefers the operator's setting, then the legacy mode recorded at upgrade, else enforce.
func ResolveScopeMode(configured, legacy string) (ScopeMode, string) {
	if strings.TrimSpace(configured) != "" {
		return ParseScopeMode(configured), "configured"
	}
	if strings.TrimSpace(legacy) != "" {
		return ParseScopeMode(legacy), "kept from before scopes were enforced"
	}
	return ScopeModeEnforce, "default"
}

// ScopeViolation describes a request whose API key did not carry the scope its route requires.
type ScopeViolation struct {
	UserID   uint
	KeyID    uint
	Required string
	Granted  []string
	Method   string
	Path     string
	IP       string
	Enforced bool
}

// RequireScope applies to API-key callers only; sessions are governed by workspace RBAC.
func RequireScope(mode ScopeMode, required string, report func(ScopeViolation)) okapi.Middleware {
	if mode == ScopeModeOff || required == "" {
		return func(c *okapi.Context) error { return c.Next() }
	}
	return func(c *okapi.Context) error {
		if c.GetString(CtxAuthMethod) != "api_key" {
			return c.Next()
		}
		granted := splitScopes(c.GetString(CtxAPIKeyScopes))
		if models.ScopeSatisfies(granted, required) {
			return c.Next()
		}
		if report != nil {
			report(ScopeViolation{
				UserID:   UserID(c),
				KeyID:    APIKeyID(c),
				Required: required,
				Granted:  granted,
				Method:   c.Request().Method,
				Path:     c.Request().URL.Path,
				IP:       c.RealIP(),
				Enforced: mode == ScopeModeEnforce,
			})
		}
		if mode == ScopeModeEnforce {
			c.SetHeader(ScopeViolationHeader, required)
			return c.AbortForbidden("API key missing required scope: " + required)
		}
		c.SetHeader(ScopeViolationHeader, required)
		return c.Next()
	}
}

func splitScopes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
