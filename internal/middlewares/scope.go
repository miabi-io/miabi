// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package middlewares

import (
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
)

// ScopeMode is how strictly API-key scopes are enforced, set by
// MIABI_API_KEY_SCOPE_ENFORCEMENT. The staged rollout exists because scopes have never been
// enforced: a key created with the default `read` scope may have been writing for months, and
// flipping straight to enforce would break it without warning.
type ScopeMode string

const (
	// ScopeModeOff skips the check entirely — the behaviour before scopes were enforced.
	ScopeModeOff ScopeMode = "off"
	// ScopeModeWarn lets the request through but records the violation, so an operator can see
	// which of their keys would break before enforcing.
	ScopeModeWarn ScopeMode = "warn"
	// ScopeModeEnforce refuses a request whose key lacks the scope.
	ScopeModeEnforce ScopeMode = "enforce"
)

// ScopeViolationHeader names the scope a warn-mode request would have needed, so a client sees the
// coming break in its own responses rather than only in the operator's audit log.
const ScopeViolationHeader = "X-Miabi-Scope-Required"

// ParseScopeMode reads a configured mode, falling back to warn for anything unrecognised — an
// operator's typo must not silently disable a security control.
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

// RequireScope guards a route for API-key callers, demanding the presented key carry a scope that
// satisfies `required` on the ladder (see models.ScopeSatisfies). Session and JWT callers are
// unaffected — their access is governed by the workspace RBAC middleware instead.
//
// `report` is called for every violation, in warn mode as well as enforce, and may be nil.
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
