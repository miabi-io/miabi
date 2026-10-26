// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

// Workspace creation modes decide who may start a new workspace. They govern creation only: owning
// one through an owner invitation stays under the per-user workspace limits.
const (
	WorkspaceCreationAnyone        = "anyone"
	WorkspaceCreationExceptInvited = "except_invited"
	WorkspaceCreationAdminsOnly    = "admins_only"
	// WorkspaceCreationConsole leaves creation to a connected billing console: nobody, admins
	// included, creates a workspace from Miabi itself.
	WorkspaceCreationConsole = "console"
)

// ValidWorkspaceCreation reports whether mode is a known workspace creation mode.
func ValidWorkspaceCreation(mode string) bool {
	switch mode {
	case WorkspaceCreationAnyone, WorkspaceCreationExceptInvited, WorkspaceCreationAdminsOnly, WorkspaceCreationConsole:
		return true
	}
	return false
}

// Signup sources record how an account came to exist. Set once at creation; empty on accounts
// created before the field existed, which are treated like any non-invited account.
const (
	SignupSourceSelf       = "self"
	SignupSourceInvitation = "invitation"
	SignupSourceAdmin      = "admin"
	SignupSourceBilling    = "billing"
)
