// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "testing"

// TestRolePermissionsMonotonic is the core safety guarantee: each higher-ranked built-in role
// is a strict superset of every lower one. That is what lets RequirePermission coexist with
// rank-based RequireRole without silently changing access.
func TestRolePermissionsMonotonic(t *testing.T) {
	order := []WorkspaceRole{WorkspaceRoleViewer, WorkspaceRoleDeveloper, WorkspaceRoleAdmin, WorkspaceRoleOwner}
	for i := 1; i < len(order); i++ {
		lower, higher := order[i-1], order[i]
		for p := range rolePermissions[lower] {
			if !rolePermissions[higher][p] {
				t.Fatalf("%s lacks %q held by lower role %s — role presets must be monotonic by rank", higher, p, lower)
			}
		}
		if len(rolePermissions[higher]) <= len(rolePermissions[lower]) {
			t.Fatalf("%s should grant strictly more than %s", higher, lower)
		}
	}
}

// TestOwnerHasEveryPermission ensures the catalog and the owner preset stay in
// sync — owner must hold every catalogued permission.
func TestOwnerHasEveryPermission(t *testing.T) {
	for _, p := range AllPermissions() {
		if !RoleHasPermission(WorkspaceRoleOwner, p) {
			t.Fatalf("owner is missing catalogued permission %q", p)
		}
	}
	if len(rolePermissions[WorkspaceRoleOwner]) != len(AllPermissions()) {
		t.Fatalf("owner preset (%d) and catalog (%d) disagree", len(rolePermissions[WorkspaceRoleOwner]), len(AllPermissions()))
	}
}

// TestRoleSpotChecks pins the rank semantics the routes rely on: viewer reads
// only, developer deploys but does not delete, admin deletes, owner governs.
func TestRoleSpotChecks(t *testing.T) {
	cases := []struct {
		role WorkspaceRole
		perm Permission
		want bool
	}{
		{WorkspaceRoleViewer, PermAppRead, true},
		{WorkspaceRoleViewer, PermAppDeploy, false},
		{WorkspaceRoleViewer, PermAppDelete, false},
		{WorkspaceRoleDeveloper, PermAppDeploy, true},
		{WorkspaceRoleDeveloper, PermAppDelete, false},
		{WorkspaceRoleDeveloper, PermMemberWrite, false},
		{WorkspaceRoleAdmin, PermAppDelete, true},
		{WorkspaceRoleAdmin, PermMemberWrite, true},
		{WorkspaceRoleAdmin, PermWorkspaceDelete, false},
		{WorkspaceRoleOwner, PermWorkspaceDelete, true},
	}
	for _, tc := range cases {
		if got := RoleHasPermission(tc.role, tc.perm); got != tc.want {
			t.Errorf("RoleHasPermission(%s, %s) = %v, want %v", tc.role, tc.perm, got, tc.want)
		}
	}
}

// TestPermissionParsing checks the resource/action split.
func TestPermissionParsing(t *testing.T) {
	if PermAppDeploy.Resource() != "app" || PermAppDeploy.Action() != "deploy" {
		t.Fatalf("parse failed: %s / %s", PermAppDeploy.Resource(), PermAppDeploy.Action())
	}
}
