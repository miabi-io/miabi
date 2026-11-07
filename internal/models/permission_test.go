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

package models

import "testing"

// Monotonic role sets let RequirePermission coexist with rank-based RequireRole without changing access.
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
