// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

// They govern creation only; owner invitations stay under the per-user workspace limits.
const (
	WorkspaceCreationAnyone        = "anyone"
	WorkspaceCreationExceptInvited = "except_invited"
	WorkspaceCreationAdminsOnly    = "admins_only"
	// Nobody, admins included, creates a workspace from Miabi itself.
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

// Empty on older accounts, which are treated like any non-invited account.
const (
	SignupSourceSelf       = "self"
	SignupSourceInvitation = "invitation"
	SignupSourceAdmin      = "admin"
	SignupSourceBilling    = "billing"
)
