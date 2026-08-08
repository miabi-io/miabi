// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package customrole manages admin-defined roles (permission sets) and enforces the cardinal RBAC rule:
// no privilege escalation. A role may never grant a permission its author does not already hold, and
// may never be assigned to give a member more than the assigning admin has.
package customrole

import (
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNameRequired      = errors.New("role name is required")
	ErrInvalidBaseRole   = errors.New("invalid base role")
	ErrInvalidPermission = errors.New("unknown permission")
	ErrNoPermissions     = errors.New("a role must grant at least one permission")
	ErrRoleNotFound      = errors.New("custom role not found")
	ErrRoleInUse         = errors.New("role is assigned to members")
)

// ErrEscalation is returned when a role would grant more than its author holds.
// It carries a stable code mapped to HTTP 403 by the handler.
var ErrEscalation = &escalationError{}

type escalationError struct{}

func (*escalationError) Error() string { return "a role cannot grant permissions you do not hold" }
func (*escalationError) Code() string  { return "ROLE_ESCALATION" }

// Service manages custom roles.
type Service struct {
	repo *repositories.CustomRoleRepository
}

func NewService(repo *repositories.CustomRoleRepository) *Service {
	return &Service{repo: repo}
}

// Input is a validated role definition.
type Input struct {
	Name        string
	BaseRole    models.WorkspaceRole
	Permissions []models.Permission
}

func (s *Service) List(workspaceID uint) ([]models.CustomRole, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// Get returns a custom role scoped to the workspace.
func (s *Service) Get(workspaceID, id uint) (*models.CustomRole, error) {
	role, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrRoleNotFound
	}
	return role, nil
}

// Create validates and persists a role. actorPerms is the creating admin's own
// effective permission set; the role's permissions (and its base-role preset)
// must be a subset of it (no escalation).
func (s *Service) Create(workspaceID uint, in Input, actorPerms map[models.Permission]bool) (*models.CustomRole, error) {
	if err := s.validate(in, actorPerms); err != nil {
		return nil, err
	}
	role := &models.CustomRole{
		WorkspaceID: &workspaceID,
		Name:        strings.TrimSpace(in.Name),
		BaseRole:    in.BaseRole,
		Permissions: toStrings(in.Permissions),
	}
	if err := s.repo.Create(role); err != nil {
		return nil, err
	}
	return role, nil
}

// Update edits a role, re-checking no-escalation against the editing admin.
func (s *Service) Update(workspaceID, id uint, in Input, actorPerms map[models.Permission]bool) (*models.CustomRole, error) {
	role, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrRoleNotFound
	}
	if err := s.validate(in, actorPerms); err != nil {
		return nil, err
	}
	role.Name = strings.TrimSpace(in.Name)
	role.BaseRole = in.BaseRole
	role.Permissions = toStrings(in.Permissions)
	if err := s.repo.Update(role); err != nil {
		return nil, err
	}
	return role, nil
}

// Delete removes a role, refusing while it is assigned to any member.
func (s *Service) Delete(workspaceID, id uint) error {
	role, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrRoleNotFound
	}
	n, err := s.repo.CountMembersUsing(role.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrRoleInUse
	}
	return s.repo.Delete(role.ID)
}

// validate checks the definition and the no-escalation invariant.
func (s *Service) validate(in Input, actorPerms map[models.Permission]bool) error {
	if strings.TrimSpace(in.Name) == "" {
		return ErrNameRequired
	}
	if !in.BaseRole.Valid() {
		return ErrInvalidBaseRole
	}
	if len(in.Permissions) == 0 {
		return ErrNoPermissions
	}
	for _, p := range in.Permissions {
		if !models.IsValidPermission(p) {
			return ErrInvalidPermission
		}
		if !actorPerms[p] {
			return ErrEscalation
		}
	}
	// The base-role rank fallback must also stay within the author's grant.
	for p := range models.RolePermissions(in.BaseRole) {
		if !actorPerms[p] {
			return ErrEscalation
		}
	}
	return nil
}

func toStrings(ps []models.Permission) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, string(p))
	}
	return out
}
