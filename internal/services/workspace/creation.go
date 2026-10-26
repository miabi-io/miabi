// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"errors"

	"github.com/miabi-io/miabi/internal/models"
)

// ErrWorkspaceCreationNotAllowed matches every CreationDeniedError.
var ErrWorkspaceCreationNotAllowed = errors.New("you are not allowed to create workspaces")

// Sources of a creation decision, reported by CreationPolicy.
const (
	CreationSourceAdmin        = "admin"
	CreationSourceUser         = "user"
	CreationSourceOrganization = "organization"
	CreationSourcePlatform     = "platform"
)

// CreationDeniedError is returned when the creation policy refuses a user. Mode is the rule that
// applied, or empty when a per-user override denied it.
type CreationDeniedError struct{ Mode string }

func (e *CreationDeniedError) Error() string {
	switch e.Mode {
	case models.WorkspaceCreationConsole:
		return "workspaces are created in Miabi Console; open the console to add one"
	case models.WorkspaceCreationAdminsOnly:
		return "only administrators can create workspaces here; ask one to create a workspace or invite you to one"
	case models.WorkspaceCreationExceptInvited:
		return "accounts created from an invitation cannot create workspaces; ask an administrator if you need one"
	default:
		return "you are not allowed to create workspaces; ask an administrator if you need one"
	}
}

func (e *CreationDeniedError) Is(target error) bool { return target == ErrWorkspaceCreationNotAllowed }

// Code is the stable API error code.
func (e *CreationDeniedError) Code() string {
	if e.Mode == models.WorkspaceCreationConsole {
		return "WORKSPACE_CREATION_CONSOLE"
	}
	return "WORKSPACE_CREATION_NOT_ALLOWED"
}

// CreationPolicy is whether a user may create a workspace, and which rule decided it.
type CreationPolicy struct {
	Allowed bool `json:"allowed"`
	// Mode is the workspace creation mode that applied; empty when Source is admin or user.
	Mode   string `json:"mode,omitempty"`
	Source string `json:"source"`
	// ConsoleURL is where workspaces are created in console mode.
	ConsoleURL string `json:"console_url,omitempty"`
}

// SetConsoleURL wires the connected billing console's address, shown in console mode.
func (s *Service) SetConsoleURL(fn func() string) { s.consoleURL = fn }

// SetCreationPolicy wires the platform workspace creation mode. The per-user override follows the
// same Enterprise entitlement as the per-user workspace limit (see SetLimits), and the organization
// mode the organizations entitlement. Nil-safe: unwired, anyone may create.
func (s *Service) SetCreationPolicy(platformMode func() string) { s.creationMode = platformMode }

// CreationPolicy resolves whether userID may create a workspace: platform admins always may, then
// the Enterprise per-user override, the home organization's mode, and the platform mode.
func (s *Service) CreationPolicy(userID uint) CreationPolicy {
	// Console mode outranks admins and per-user overrides: a workspace made here would go unbilled.
	if s.ConsoleOnly() {
		p := CreationPolicy{Allowed: false, Mode: models.WorkspaceCreationConsole, Source: CreationSourcePlatform}
		if s.consoleURL != nil {
			p.ConsoleURL = s.consoleURL()
		}
		return p
	}
	u, err := s.users.FindByID(userID)
	if err != nil {
		return CreationPolicy{Allowed: true, Mode: models.WorkspaceCreationAnyone, Source: CreationSourcePlatform}
	}
	if u.IsAdmin() {
		return CreationPolicy{Allowed: true, Source: CreationSourceAdmin}
	}
	if s.overrideEntitled != nil && s.overrideEntitled() && u.CanCreateWorkspaces != nil {
		return CreationPolicy{Allowed: *u.CanCreateWorkspaces, Source: CreationSourceUser}
	}
	mode, source := models.WorkspaceCreationAnyone, CreationSourcePlatform
	if s.creationMode != nil {
		if m := s.creationMode(); models.ValidWorkspaceCreation(m) {
			mode = m
		}
	}
	if org := s.orgLimits(userID); org != nil && org.WorkspaceCreation != nil && models.ValidWorkspaceCreation(*org.WorkspaceCreation) {
		mode, source = *org.WorkspaceCreation, CreationSourceOrganization
	}
	return CreationPolicy{Allowed: s.modeAllows(mode, u), Mode: mode, Source: source}
}

// ConsoleOnly reports whether the platform leaves workspace creation to a billing console.
func (s *Service) ConsoleOnly() bool {
	return s.creationMode != nil && s.creationMode() == models.WorkspaceCreationConsole
}

func (s *Service) modeAllows(mode string, u *models.User) bool {
	switch mode {
	case models.WorkspaceCreationConsole:
		return false
	case models.WorkspaceCreationExceptInvited:
		return u.SignupSource != models.SignupSourceInvitation
	case models.WorkspaceCreationAdminsOnly:
		org := s.homeOrg(u.ID)
		return org != nil && org.OwnerUserID == u.ID
	default:
		return true
	}
}

// CanCreate returns a CreationDeniedError when the creation policy refuses userID. Service accounts
// are left to canOwnAnother, which refuses them with a clearer error.
func (s *Service) CanCreate(userID uint) error {
	if u, err := s.users.FindByID(userID); err == nil && u.IsService() {
		return nil
	}
	p := s.CreationPolicy(userID)
	if p.Allowed {
		return nil
	}
	return &CreationDeniedError{Mode: p.Mode}
}
