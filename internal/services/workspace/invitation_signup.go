// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"errors"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// InvitationPreview is what the holder of an invitation token may see before accepting it.
type InvitationPreview struct {
	WorkspaceName string               `json:"workspace_name"`
	InvitedByName string               `json:"invited_by_name"`
	Role          models.WorkspaceRole `json:"role"`
	Email         string               `json:"email"`
	ExpiresAt     time.Time            `json:"expires_at"`
	AccountExists bool                 `json:"account_exists"`
	// OrganizationID is the realm the workspace belongs to; 0 for the default one.
	OrganizationID uint `json:"-"`
}

// PendingInvitationByToken returns the invitation for a raw token while it can still be accepted.
func (s *Service) PendingInvitationByToken(rawToken string) (*models.WorkspaceInvitation, error) {
	inv, err := s.repo.FindInvitationByHash(hashToken(rawToken))
	if err != nil || inv.Status != models.InvitationStatusPending || time.Now().After(inv.ExpiresAt) {
		return nil, ErrInvalidInvite
	}
	return inv, nil
}

// PreviewInvitation describes a pending invitation to the holder of its token.
func (s *Service) PreviewInvitation(rawToken string) (*InvitationPreview, error) {
	inv, err := s.PendingInvitationByToken(rawToken)
	if err != nil {
		return nil, err
	}
	ws, err := s.repo.FindByID(inv.WorkspaceID)
	if err != nil {
		return nil, ErrInvalidInvite
	}
	p := &InvitationPreview{
		WorkspaceName: ws.DisplayName, Role: inv.Role, Email: inv.Email, ExpiresAt: inv.ExpiresAt,
	}
	if ws.OrganizationID != nil {
		p.OrganizationID = *ws.OrganizationID
	}
	if u, err := s.users.FindByID(inv.InvitedBy); err == nil {
		p.InvitedByName = u.Name
	}
	if exists, err := s.users.ExistsByEmail(inv.Email); err == nil {
		p.AccountExists = exists
	}
	return p, nil
}

// Every refusal that would reveal whether the address is registered is ErrInvalidInvite.
func (s *Service) RegisterFromInvitation(rawToken string, user *models.User) (*models.Workspace, error) {
	inv, err := s.PendingInvitationByToken(rawToken)
	if err != nil {
		return nil, err
	}
	if exists, err := s.users.ExistsByEmail(inv.Email); err != nil || exists {
		return nil, ErrInvalidInvite
	}
	ws, err := s.repo.FindByID(inv.WorkspaceID)
	if err != nil {
		return nil, ErrInvalidInvite
	}
	if err := s.checkMemberCapacity(inv.WorkspaceID); err != nil {
		return nil, err
	}
	user.Email = inv.Email
	user.SignupSource = models.SignupSourceInvitation
	user.Kind = models.UserKindHuman
	user.OrganizationID = ws.OrganizationID
	if err := s.repo.CreateUserFromInvitation(user, inv); err != nil {
		if errors.Is(err, repositories.ErrInvitationClaimed) {
			return nil, ErrInvalidInvite
		}
		if exists, e := s.users.ExistsByEmail(inv.Email); e == nil && exists {
			return nil, ErrInvalidInvite
		}
		return nil, err
	}
	return ws, nil
}
