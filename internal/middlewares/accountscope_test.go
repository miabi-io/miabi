// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package middlewares

import "testing"

// The allow-list is written against the path after the API version prefix, so a route's full URL has
// to reduce to it — including with a trailing slash or a different mount point.
func TestAccountPathReduction(t *testing.T) {
	cases := map[string]string{
		"/api/v1/me":                   "/me",
		"/api/v1/me/":                  "/me",
		"/api/v1/permissions":          "/permissions",
		"/api/v1/capabilities":         "/capabilities",
		"/api/v1/api-keys":             "/api-keys",
		"/api/v1/me/sessions":          "/me/sessions",
		"/api/v1/workspaces/7/volumes": "/workspaces/7/volumes",
		"/me":                          "/me",
	}
	for in, want := range cases {
		if got := reducePath(in); got != want {
			t.Errorf("reducePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// Only the identity endpoints are readable by a workspace-bound key; everything else account-level
// is refused, which is the whole point.
func TestAccountReadableList(t *testing.T) {
	for _, allowed := range []string{"/me", "/permissions", "/capabilities"} {
		if !accountReadable[allowed] {
			t.Errorf("%s should be readable by a bound key", allowed)
		}
	}
	// The escapes from the finding: minting a key, session control, the cross-workspace inbox and
	// workspace management must never be on this list.
	for _, denied := range []string{"/api-keys", "/me/sessions", "/me/sessions/revoke-others", "/workspaces", "/inbox", "/users"} {
		if accountReadable[denied] {
			t.Errorf("%s must not be reachable by a workspace-bound key", denied)
		}
	}
}

// The escapes the finding lists, each checked against the real path and method. A workspace-bound
// key reached every one of these before, because the binding was only ever compared on routes that
// name a workspace — and none of these do.
func TestWorkspaceBoundKeyCannotLeaveItsWorkspace(t *testing.T) {
	cases := []struct {
		name      string
		workspace string
		method    string
		path      string
		allow     bool
	}{
		// The escapes.
		{"mint a fresh unbound key", "", "POST", "/api/v1/api-keys", false},
		{"list account keys", "", "GET", "/api/v1/api-keys", false},
		{"enrol TOTP", "", "POST", "/api/v1/auth/2fa/setup", false},
		{"take recovery codes", "", "POST", "/api/v1/auth/2fa/recovery-codes", false},
		{"revoke every session", "", "POST", "/api/v1/me/sessions/revoke-others", false},
		{"list sessions", "", "GET", "/api/v1/me/sessions", false},
		{"platform admin", "", "GET", "/api/v1/admin/users", false},
		{"create a workspace", "", "POST", "/api/v1/workspaces", false},
		{"cross-workspace inbox", "", "GET", "/api/v1/inbox", false},
		{"update the account", "", "PUT", "/api/v1/me", false},

		// What a bound key is for: anything inside the workspace it names. WorkspaceScope compares
		// the binding there, so a mismatch is still refused — one layer down, not here.
		{"its own workspace", "acme", "GET", "/api/v1/workspaces/acme/volumes", true},
		{"a write in its workspace", "acme", "POST", "/api/v1/workspaces/acme/applications", true},
		{"another workspace, caught by WorkspaceScope", "other", "GET", "/api/v1/workspaces/other/volumes", true},

		// Identity reads stay open so a CI script can find out who it is.
		{"who am i", "", "GET", "/api/v1/me", true},
		{"permissions", "", "GET", "/api/v1/permissions", true},
		{"capabilities", "", "GET", "/api/v1/capabilities", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := workspaceKeyAllowed(c.workspace, c.method, c.path); got != c.allow {
				t.Fatalf("workspaceKeyAllowed(%q, %s %s) = %v, want %v", c.workspace, c.method, c.path, got, c.allow)
			}
		})
	}
}
