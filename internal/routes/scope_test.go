// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// The golden list: routes whose classification is the point of the fix. A GET that hands back a
// credential, a key or a shell must not fall through to `read` just because it is a GET, and the
// account- and platform-administration trees must be out of reach of an ordinary key.
func TestScopeFor_SensitiveRoutes(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/workspaces/{workspace}/apps/{appID}/exec"},
		{"GET", "/api/v1/workspaces/{workspace}/apps/{appID}/env/{key}/reveal"},
		{"GET", "/api/v1/workspaces/{workspace}/secrets/{secretID}/reveal"},
		{"GET", "/api/v1/workspaces/{workspace}/configs/{configID}/reveal"},
		{"GET", "/api/v1/workspaces/{workspace}/databases/{databaseID}/credentials"},
		{"GET", "/api/v1/workspaces/{workspace}/apps/{appID}/databases/{dbID}/connection"},
		{"GET", "/api/v1/workspaces/{workspace}/databases/{databaseID}/databases/{dbID}/connection"},
		{"GET", "/api/v1/workspaces/{workspace}/gitops/{pipelineID}/webhook-info"},
		{"GET", "/api/v1/workspaces/{workspace}/backups/sets/{setID}/recovery-kit"},
		// Platform and account administration.
		{"GET", "/api/v1/admin/users"},
		{"POST", "/api/v1/admin/organizations"},
		{"GET", "/api/v1/admin/platform-backup/backups/{id}/download"},
		{"GET", "/api/v1/api-keys"},
		{"POST", "/api/v1/api-keys"},
		{"POST", "/api/v1/auth/2fa/disable"},
		{"GET", "/api/v1/me/sessions"},
		// Who may act in a workspace, and what it recorded.
		{"GET", "/api/v1/workspaces/{workspace}/members"},
		{"POST", "/api/v1/workspaces/{workspace}/invitations"},
		{"POST", "/api/v1/workspaces/{workspace}/roles"},
		{"GET", "/api/v1/workspaces/{workspace}/audit-logs"},
		{"GET", "/api/v1/workspaces/{workspace}/audit/export"},
		{"POST", "/api/v1/workspaces/{workspace}/apps/{appID}/policies"},
		// Destroying or replacing a whole workspace.
		{"DELETE", "/api/v1/workspaces/{workspace}"},
		{"POST", "/api/v1/workspaces/{workspace}/portable-backup/restore"},
		{"PUT", "/api/v1/workspaces/{workspace}/backup-settings"},
		// Reconfiguring what builds code or what the platform calls out to.
		{"POST", "/api/v1/workspaces/{workspace}/runners"},
		{"DELETE", "/api/v1/workspaces/{workspace}/webhooks/{id}"},
		// A backup artifact is the workspace's data in one file.
		{"GET", "/api/v1/workspaces/{workspace}/databases/{databaseID}/databases/{dbID}/backups/{backupID}/download"},
	} {
		if got := scopeFor(tc.method, tc.path); got != models.ScopeAdmin {
			t.Errorf("scopeFor(%s %s) = %q, want %q", tc.method, tc.path, got, models.ScopeAdmin)
		}
	}
}

func TestScopeFor_Defaults(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		// Ordinary reads.
		{"GET", "/api/v1/workspaces/{workspace}/apps", models.ScopeRead},
		{"GET", "/api/v1/workspaces/{workspace}/apps/{appID}", models.ScopeRead},
		{"GET", "/api/v1/workspaces", models.ScopeRead},
		{"GET", "/api/v1/me", models.ScopeRead},
		{"GET", "/api/v1/permissions", models.ScopeRead},
		{"HEAD", "/api/v1/workspaces/{workspace}/apps", models.ScopeRead},
		// Reading a runner or a webhook is ordinary; writing one is not.
		{"GET", "/api/v1/workspaces/{workspace}/runners", models.ScopeRead},
		{"GET", "/api/v1/workspaces/{workspace}/webhooks", models.ScopeRead},
		// Deployment lifecycle.
		{"POST", "/api/v1/workspaces/{workspace}/apps/{appID}/deploy", models.ScopeDeploy},
		{"POST", "/api/v1/workspaces/{workspace}/apps/{appID}/restart", models.ScopeDeploy},
		{"POST", "/api/v1/workspaces/{workspace}/apps/{appID}/stop", models.ScopeDeploy},
		{"POST", "/api/v1/workspaces/{workspace}/apps/{appID}/scale", models.ScopeDeploy},
		{"POST", "/api/v1/workspaces/{workspace}/apps/{appID}/rollback", models.ScopeDeploy},
		{"POST", "/api/v1/workspaces/{workspace}/apps/{appID}/canary/promote", models.ScopeDeploy},
		{"POST", "/api/v1/workspaces/{workspace}/gitops/{id}/sync", models.ScopeDeploy},
		{"POST", "/api/v1/workspaces/{workspace}/pipelines/{id}/trigger", models.ScopeDeploy},
		{"POST", "/api/v1/workspaces/{workspace}/pipelines/runs/{id}/rerun", models.ScopeDeploy},
		// Ordinary mutations.
		{"POST", "/api/v1/workspaces/{workspace}/apps", models.ScopeWrite},
		{"PUT", "/api/v1/workspaces/{workspace}/apps/{appID}", models.ScopeWrite},
		{"DELETE", "/api/v1/workspaces/{workspace}/apps/{appID}", models.ScopeWrite},
		{"POST", "/api/v1/workspaces/{workspace}/secrets", models.ScopeWrite},
		{"PATCH", "/api/v1/workspaces/{workspace}/networks/{id}", models.ScopeWrite},
		// A log download is not a backup download.
		{"GET", "/api/v1/workspaces/{workspace}/apps/{appID}/deployments/{deploymentID}/logs/download", models.ScopeRead},
		{"GET", "/api/v1/workspaces/{workspace}/apps/jobs/{jobID}/logs/download", models.ScopeRead},
	} {
		if got := scopeFor(tc.method, tc.path); got != tc.want {
			t.Errorf("scopeFor(%s %s) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

// Creating a workspace is an ordinary write; deleting one is not. The DELETE rule keys on the
// exact shape of the collection route, so a deeper path must not pick it up.
func TestScopeFor_WorkspaceDeleteIsExact(t *testing.T) {
	if got := scopeFor("POST", "/api/v1/workspaces"); got != models.ScopeWrite {
		t.Errorf("creating a workspace = %q, want %q", got, models.ScopeWrite)
	}
	if got := scopeFor("DELETE", "/api/v1/workspaces/{workspace}/networks/{id}"); got != models.ScopeWrite {
		t.Errorf("deleting a network = %q, want %q", got, models.ScopeWrite)
	}
}

// A path segment a tenant chooses — a workspace named `members`, an app named `deploy` — may raise
// what a route demands but must never lower it. Only the raising direction is safe.
func TestScopeFor_TenantNamesNeverLower(t *testing.T) {
	// The sensitive suffix survives a tenant-chosen segment earlier in the path.
	if got := scopeFor("GET", "/api/v1/workspaces/read/secrets/{secretID}/reveal"); got != models.ScopeAdmin {
		t.Errorf("reveal under a workspace named `read` = %q, want %q", got, models.ScopeAdmin)
	}
	// And a tenant-chosen name that collides with an admin segment only raises the bar.
	if got := scopeFor("GET", "/api/v1/workspaces/members/apps"); got != models.ScopeAdmin {
		t.Errorf("workspace named `members` = %q, want %q (raising is the safe direction)", got, models.ScopeAdmin)
	}
}
