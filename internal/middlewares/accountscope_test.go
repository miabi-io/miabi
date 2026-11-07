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

import "testing"

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

func TestAccountReadableList(t *testing.T) {
	for _, allowed := range []string{"/me", "/permissions", "/capabilities"} {
		if !accountReadable[allowed] {
			t.Errorf("%s should be readable by a bound key", allowed)
		}
	}
	for _, denied := range []string{"/api-keys", "/me/sessions", "/me/sessions/revoke-others", "/workspaces", "/inbox", "/users"} {
		if accountReadable[denied] {
			t.Errorf("%s must not be reachable by a workspace-bound key", denied)
		}
	}
}

// Bound keys escaped before because the binding was only compared on routes naming a workspace.
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

		// WorkspaceScope compares the binding there, so a mismatch is still refused one layer down.
		{"its own workspace", "acme", "GET", "/api/v1/workspaces/acme/volumes", true},
		{"a write in its workspace", "acme", "POST", "/api/v1/workspaces/acme/applications", true},
		{"another workspace, caught by WorkspaceScope", "other", "GET", "/api/v1/workspaces/other/volumes", true},
		{"a migration key listing what it opens", "", "GET", "/api/v1/migration/workspaces", true},
		{"the import endpoint is not the migration tree", "", "POST", "/api/v1/migrations", false},

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
