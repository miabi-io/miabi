// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "strings"

// Permission is a fine-grained capability of the form "resource:action". Each built-in
// WorkspaceRole maps to a permission set reproducing rank-based RequireRole exactly (enforced
// by permission_test.go); custom roles are just alternative permission sets.
type Permission string

const (
	PermAppRead   Permission = "app:read"
	PermAppWrite  Permission = "app:write"  // create / update config / env / attach
	PermAppDeploy Permission = "app:deploy" // deploy / start / stop / restart / rollback / canary
	PermAppDelete Permission = "app:delete"
	PermAppAdmin  Permission = "app:admin" // reveal secrets, privileged host mounts

	PermDatabaseRead   Permission = "database:read"
	PermDatabaseWrite  Permission = "database:write"
	PermDatabaseDelete Permission = "database:delete"

	PermVolumeRead  Permission = "volume:read"
	PermVolumeWrite Permission = "volume:write"

	PermDomainRead  Permission = "domain:read"
	PermDomainWrite Permission = "domain:write"

	PermRouteRead  Permission = "route:read"
	PermRouteWrite Permission = "route:write"

	PermNetworkRead  Permission = "network:read"
	PermNetworkWrite Permission = "network:write"

	PermBackupRead    Permission = "backup:read"
	PermBackupRun     Permission = "backup:run"
	PermBackupRestore Permission = "backup:restore"

	PermSecretRead  Permission = "secret:read"
	PermSecretWrite Permission = "secret:write"

	PermConfigRead  Permission = "config:read"
	PermConfigWrite Permission = "config:write"

	PermMemberRead  Permission = "member:read"
	PermMemberWrite Permission = "member:write"

	PermWorkspaceWrite  Permission = "workspace:write"
	PermWorkspaceDelete Permission = "workspace:delete"
)

// Resource returns the resource segment of a permission ("app" for "app:deploy").
func (p Permission) Resource() string {
	if i := strings.IndexByte(string(p), ':'); i >= 0 {
		return string(p)[:i]
	}
	return string(p)
}

// Action returns the action segment ("deploy" for "app:deploy").
func (p Permission) Action() string {
	if i := strings.IndexByte(string(p), ':'); i >= 0 {
		return string(p)[i+1:]
	}
	return ""
}

// allReadPermissions is the viewer baseline: read on every resource.
var allReadPermissions = []Permission{
	PermAppRead, PermDatabaseRead, PermVolumeRead, PermDomainRead,
	PermRouteRead, PermNetworkRead, PermBackupRead, PermSecretRead, PermConfigRead, PermMemberRead,
}

// developerExtra adds building and running apps over a viewer, but nothing destructive.
var developerExtra = []Permission{
	PermAppWrite, PermAppDeploy,
	PermDatabaseWrite, PermVolumeWrite, PermRouteWrite, PermNetworkWrite,
	PermBackupRun, PermBackupRestore, PermSecretWrite, PermConfigWrite,
}

// adminExtra adds deletes, secret reveal, privileged mounts, domains and member management.
var adminExtra = []Permission{
	PermAppDelete, PermAppAdmin, PermDatabaseDelete, PermDomainWrite, PermMemberWrite,
}

// ownerExtra is what an owner adds over an admin: workspace governance.
var ownerExtra = []Permission{
	PermWorkspaceWrite, PermWorkspaceDelete,
}

// rolePermissions is the permission set granted by each built-in role. It is
// strictly monotonic by rank (owner ⊇ admin ⊇ developer ⊇ viewer), reproducing
// the AtLeast() semantics RequireRole relies on. permission_test.go enforces this.
var rolePermissions = func() map[WorkspaceRole]map[Permission]bool {
	viewer := allReadPermissions
	developer := append(append([]Permission{}, viewer...), developerExtra...)
	admin := append(append([]Permission{}, developer...), adminExtra...)
	owner := append(append([]Permission{}, admin...), ownerExtra...)
	toSet := func(ps []Permission) map[Permission]bool {
		m := make(map[Permission]bool, len(ps))
		for _, p := range ps {
			m[p] = true
		}
		return m
	}
	return map[WorkspaceRole]map[Permission]bool{
		WorkspaceRoleViewer:    toSet(viewer),
		WorkspaceRoleDeveloper: toSet(developer),
		WorkspaceRoleAdmin:     toSet(admin),
		WorkspaceRoleOwner:     toSet(owner),
	}
}()

// RoleHasPermission reports whether a built-in role grants a permission.
func RoleHasPermission(role WorkspaceRole, p Permission) bool {
	return rolePermissions[role][p]
}

// RolePermissions returns the permission set of a built-in role as a copy.
func RolePermissions(role WorkspaceRole) map[Permission]bool {
	src := rolePermissions[role]
	out := make(map[Permission]bool, len(src))
	for p := range src {
		out[p] = true
	}
	return out
}

// AllPermissions returns the full catalog in a stable, grouped order (for the UI
// and the permission-picker in custom roles).
func AllPermissions() []Permission {
	return []Permission{
		PermAppRead, PermAppWrite, PermAppDeploy, PermAppDelete, PermAppAdmin,
		PermDatabaseRead, PermDatabaseWrite, PermDatabaseDelete,
		PermVolumeRead, PermVolumeWrite,
		PermDomainRead, PermDomainWrite,
		PermRouteRead, PermRouteWrite,
		PermNetworkRead, PermNetworkWrite,
		PermBackupRead, PermBackupRun, PermBackupRestore,
		PermSecretRead, PermSecretWrite,
		PermConfigRead, PermConfigWrite,
		PermMemberRead, PermMemberWrite,
		PermWorkspaceWrite, PermWorkspaceDelete,
	}
}

var validPermissions = func() map[Permission]bool {
	m := make(map[Permission]bool)
	for _, p := range AllPermissions() {
		m[p] = true
	}
	return m
}()

// IsValidPermission reports whether p is a known catalogued permission.
func IsValidPermission(p Permission) bool { return validPermissions[p] }
