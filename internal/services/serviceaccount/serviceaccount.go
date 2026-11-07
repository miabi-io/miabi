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

// Package serviceaccount manages workspace-owned non-human users that sign in only with API keys.
package serviceaccount

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound    = errors.New("service account not found")
	ErrInvalidName = errors.New("name is required (at most 64 characters)")
	ErrInvalidRole = errors.New("role must be viewer, developer or admin")
	ErrKeyNotFound = errors.New("API key not found")
)

// Service creates service accounts, their memberships and their keys.
type Service struct {
	users      *repositories.UserRepository
	workspaces *repositories.WorkspaceRepository
	keys       *auth.APIKeyService
	keyRepo    *repositories.APIKeyRepository
}

func NewService(users *repositories.UserRepository, workspaces *repositories.WorkspaceRepository, keys *auth.APIKeyService, keyRepo *repositories.APIKeyRepository) *Service {
	return &Service{users: users, workspaces: workspaces, keys: keys, keyRepo: keyRepo}
}

// assignableRole rejects owner: ownership is accountability, which a machine cannot hold.
func assignableRole(r models.WorkspaceRole) (models.WorkspaceRole, error) {
	if r == "" {
		return models.WorkspaceRoleDeveloper, nil
	}
	if !r.Valid() || r == models.WorkspaceRoleOwner {
		return "", ErrInvalidRole
	}
	return r, nil
}

// Create adds a service account to workspaceID with the given role.
func (s *Service) Create(workspaceID uint, name string, role models.WorkspaceRole) (*models.User, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, ErrInvalidName
	}
	role, err := assignableRole(role)
	if err != nil {
		return nil, err
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	handle := "sa-" + slug.Make(name, "bot")
	if len(handle) > 40 {
		handle = handle[:40]
	}
	handle = strings.TrimRight(handle, "-") + "-" + hex.EncodeToString(suffix)
	ws := workspaceID
	u := &models.User{
		Name:               name,
		Username:           handle,
		Email:              handle + "@" + models.ServiceAccountEmailDomain,
		PasswordHash:       "!", // not a bcrypt hash, so no password can ever match
		Role:               models.SystemRoleUser,
		Active:             true,
		AuthSource:         models.AuthSourceLocal,
		Kind:               models.UserKindService,
		ServiceWorkspaceID: &ws,
	}
	if err := s.users.Create(u); err != nil {
		return nil, fmt.Errorf("create service account: %w", err)
	}
	if err := s.workspaces.AddMember(&models.WorkspaceMember{WorkspaceID: workspaceID, UserID: u.ID, Role: role}); err != nil {
		u.Active = false
		_ = s.users.Update(u)
		return nil, fmt.Errorf("add service account to workspace: %w", err)
	}
	return u, nil
}

// List returns the service accounts workspaceID owns.
func (s *Service) List(workspaceID uint) ([]models.User, error) {
	return s.users.ListServiceAccounts(workspaceID)
}

// Get returns a service account owned by workspaceID.
func (s *Service) Get(workspaceID, id uint) (*models.User, error) {
	u, err := s.users.FindByID(id)
	if err != nil || !u.IsService() || !u.Active || u.ServiceWorkspaceID == nil || *u.ServiceWorkspaceID != workspaceID {
		return nil, ErrNotFound
	}
	return u, nil
}

// Delete keeps the row so audit entries keep naming who acted.
func (s *Service) Delete(workspaceID, id uint) (*models.User, error) {
	u, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if err := s.keyRepo.RevokeAllByUser(u.ID); err != nil {
		return nil, fmt.Errorf("revoke keys: %w", err)
	}
	if err := s.workspaces.RemoveAllMemberships(u.ID); err != nil {
		return nil, fmt.Errorf("remove memberships: %w", err)
	}
	u.Active = false
	if err := s.users.Update(u); err != nil {
		return nil, err
	}
	return u, nil
}

// KeyRequest describes a key to mint for a service account.
type KeyRequest struct {
	Name       string
	Scopes     []string
	AllowedIPs []string
	ExpiresAt  *time.Time
}

// CreateKey mints an API key for a service account owned by workspaceID.
func (s *Service) CreateKey(workspaceID, id uint, req KeyRequest) (string, *models.APIKey, error) {
	u, err := s.Get(workspaceID, id)
	if err != nil {
		return "", nil, err
	}
	scopes, err := auth.NormalizeScopes(req.Scopes)
	if err != nil {
		return "", nil, err
	}
	// Always bound: the account belongs to this workspace alone.
	ws := workspaceID
	return s.keys.Create(u.ID, &ws, req.Name, req.AllowedIPs, scopes, req.ExpiresAt)
}

// ListKeys returns a service account's keys.
func (s *Service) ListKeys(workspaceID, id uint) ([]models.APIKey, error) {
	if _, err := s.Get(workspaceID, id); err != nil {
		return nil, err
	}
	return s.keyRepo.ListByUser(id)
}

// RevokeKey revokes one of a service account's keys.
func (s *Service) RevokeKey(workspaceID, id, keyID uint) (*models.APIKey, error) {
	if _, err := s.Get(workspaceID, id); err != nil {
		return nil, err
	}
	k, err := s.keyRepo.FindByID(keyID)
	if err != nil || k.UserID != id {
		return nil, ErrKeyNotFound
	}
	return k, s.keyRepo.Revoke(k.ID)
}
