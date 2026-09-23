// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
)

// call drives one request through RequireScope with the context an authenticated API-key request
// would carry, and reports the status, whether the handler ran, and any violation reported.
func call(t *testing.T, mode ScopeMode, required, authMethod, scopes string) (int, bool, http.Header, []ScopeViolation) {
	t.Helper()
	var seen []ScopeViolation
	reached := false

	app := okapi.New()
	seed := func(c *okapi.Context) error {
		if authMethod != "" {
			c.Set(CtxAuthMethod, authMethod)
			c.Set(CtxAPIKeyScopes, scopes)
			c.Set(CtxAPIKeyID, 7)
			c.Set(CtxUserID, 3)
		}
		return c.Next()
	}
	guard := RequireScope(mode, required, func(v ScopeViolation) { seen = append(seen, v) })
	app.Get("/api/v1/thing", func(c *okapi.Context) error {
		reached = true
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	}, okapi.UseMiddleware(seed), okapi.UseMiddleware(guard))

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil))
	return rec.Code, reached, rec.Header(), seen
}

// Enforce is the mode that actually closes the hole: a key without the scope is refused before the
// handler runs.
func TestRequireScope_EnforceRefuses(t *testing.T) {
	code, reached, _, seen := call(t, ScopeModeEnforce, models.ScopeWrite, "api_key", "read")
	if code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", code)
	}
	if reached {
		t.Error("the handler ran despite the missing scope")
	}
	if len(seen) != 1 || !seen[0].Enforced {
		t.Errorf("violations = %+v, want one enforced", seen)
	}
}

// Warn is the rollout mode: the request succeeds, but it leaves evidence in both the audit sink and
// the client's own response, so nobody is surprised when enforce is switched on.
func TestRequireScope_WarnPassesButReports(t *testing.T) {
	code, reached, hdr, seen := call(t, ScopeModeWarn, models.ScopeWrite, "api_key", "read")
	if code != http.StatusOK || !reached {
		t.Errorf("status = %d, handler reached = %v; warn must not break the request", code, reached)
	}
	if got := hdr.Get(ScopeViolationHeader); got != models.ScopeWrite {
		t.Errorf("%s = %q, want %q", ScopeViolationHeader, got, models.ScopeWrite)
	}
	if len(seen) != 1 || seen[0].Enforced {
		t.Errorf("violations = %+v, want one unenforced", seen)
	}
	if seen[0].KeyID != 7 || seen[0].UserID != 3 {
		t.Errorf("violation identifies key %d user %d, want 7/3", seen[0].KeyID, seen[0].UserID)
	}
}

// Off is the pre-1.11 behaviour, kept so an operator can get out of the way of a bad classification
// without downgrading.
func TestRequireScope_OffIsSilent(t *testing.T) {
	code, reached, hdr, seen := call(t, ScopeModeOff, models.ScopeAdmin, "api_key", "read")
	if code != http.StatusOK || !reached {
		t.Errorf("status = %d, reached = %v; off must not interfere", code, reached)
	}
	if hdr.Get(ScopeViolationHeader) != "" {
		t.Error("off mode set the violation header")
	}
	if len(seen) != 0 {
		t.Errorf("off mode reported %d violations", len(seen))
	}
}

// A key that satisfies the requirement through the ladder passes silently.
func TestRequireScope_LadderSatisfies(t *testing.T) {
	for _, tc := range []struct{ granted, required string }{
		{"admin", models.ScopeWrite},
		{"*", models.ScopeAdmin},
		{"write", models.ScopeRead},
		{"deploy", models.ScopeDeploy},
		{"read,write,deploy", models.ScopeDeploy},
	} {
		code, reached, _, seen := call(t, ScopeModeEnforce, tc.required, "api_key", tc.granted)
		if code != http.StatusOK || !reached || len(seen) != 0 {
			t.Errorf("granted %q required %q: status %d reached %v violations %d", tc.granted, tc.required, code, reached, len(seen))
		}
	}
}

// Session and JWT callers are governed by workspace RBAC, not by scopes — the console must not
// start failing because it holds no API key.
func TestRequireScope_IgnoresSessionCallers(t *testing.T) {
	code, reached, _, seen := call(t, ScopeModeEnforce, models.ScopeAdmin, "jwt", "")
	if code != http.StatusOK || !reached || len(seen) != 0 {
		t.Errorf("a JWT caller was scope-checked: status %d reached %v violations %d", code, reached, len(seen))
	}
	code, reached, _, _ = call(t, ScopeModeEnforce, models.ScopeAdmin, "", "")
	if code != http.StatusOK || !reached {
		t.Errorf("an unauthenticated route was scope-checked: status %d reached %v", code, reached)
	}
}

// An empty scope set means read-only, so it must be refused on a write — this is the CLI's
// read-only MCP default, which had no server backing at all before.
func TestRequireScope_EmptyScopeSetIsReadOnly(t *testing.T) {
	code, _, _, _ := call(t, ScopeModeEnforce, models.ScopeWrite, "api_key", "")
	if code != http.StatusForbidden {
		t.Errorf("empty scope set on a write = %d, want 403", code)
	}
	code, _, _, _ = call(t, ScopeModeEnforce, models.ScopeRead, "api_key", "")
	if code != http.StatusOK {
		t.Errorf("empty scope set on a read = %d, want 200", code)
	}
}

func TestParseScopeMode(t *testing.T) {
	for in, want := range map[string]ScopeMode{
		"off": ScopeModeOff, "OFF": ScopeModeOff,
		"enforce": ScopeModeEnforce, " Enforce ": ScopeModeEnforce,
		"warn": ScopeModeWarn, "": ScopeModeWarn,
		// A typo must not silently disable the control.
		"enfroce": ScopeModeWarn, "true": ScopeModeWarn,
	} {
		if got := ParseScopeMode(in); got != want {
			t.Errorf("ParseScopeMode(%q) = %q, want %q", in, got, want)
		}
	}
}
