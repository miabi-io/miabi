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

package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Stored KeyPrefix is keyPrefix plus 8 hex chars, for an indexed lookup before the constant-time compare.
const keyPrefix = "mb_"

// prefixLen is the length of the stored, indexed lookup prefix.
const prefixLen = len(keyPrefix) + 8

var ErrInvalidAPIKey = errors.New("invalid API key")

type APIKeyService struct {
	keys  *repositories.APIKeyRepository
	quota *quota.Service
}

func NewAPIKeyService(keys *repositories.APIKeyRepository) *APIKeyService {
	return &APIKeyService{keys: keys}
}

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *APIKeyService) SetQuota(q *quota.Service) { s.quota = q }

// An empty set defaults to read-only.
func NormalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return []string{models.ScopeRead}, nil
	}
	seen := make(map[string]bool, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		if !models.ValidScopes[sc] {
			return nil, fmt.Errorf("unknown scope: %q", sc)
		}
		if seen[sc] {
			continue
		}
		seen[sc] = true
		out = append(out, sc)
	}
	return out, nil
}

func (s *APIKeyService) Create(userID uint, workspaceID *uint, name string, allowedIPs, scopes []string, expiresAt *time.Time) (plaintext string, key *models.APIKey, err error) {
	normScopes, err := NormalizeScopes(scopes)
	if err != nil {
		return "", nil, err
	}
	// Personal keys are not counted against a workspace plan.
	if workspaceID != nil && s.quota.Enabled() {
		n, _ := s.keys.CountByWorkspace(*workspaceID)
		if err := s.quota.CheckCreate(*workspaceID, quota.ResourceAPIKeys, int(n)); err != nil {
			return "", nil, err
		}
	}

	raw, _ := generateToken() // 64 hex chars of entropy
	plaintext = keyPrefix + raw
	hash := hashToken(plaintext)

	key = &models.APIKey{
		UserID:      userID,
		WorkspaceID: workspaceID,
		Name:        name,
		KeyHash:     hash,
		KeyPrefix:   plaintext[:prefixLen],
		Scopes:      normScopes,
		AllowedIPs:  allowedIPs,
		ExpiresAt:   expiresAt,
	}
	if err := s.keys.Create(key); err != nil {
		return "", nil, err
	}
	return plaintext, key, nil
}

// CreateBilling bypasses NormalizeScopes because users can never pick the billing scope.
func (s *APIKeyService) CreateBilling(userID uint, name string, allowedIPs []string) (plaintext string, key *models.APIKey, err error) {
	raw, _ := generateToken()
	plaintext = keyPrefix + raw
	key = &models.APIKey{
		UserID:     userID,
		Name:       name,
		KeyHash:    hashToken(plaintext),
		KeyPrefix:  plaintext[:prefixLen],
		Scopes:     []string{models.ScopeBilling},
		AllowedIPs: allowedIPs,
	}
	if err := s.keys.Create(key); err != nil {
		return "", nil, err
	}
	return plaintext, key, nil
}

// CreateMigration bypasses NormalizeScopes because users can never pick the migration scope.
func (s *APIKeyService) CreateMigration(userID uint, workspaceID *uint, name string, expiresAt time.Time) (plaintext string, key *models.APIKey, err error) {
	raw, _ := generateToken()
	plaintext = keyPrefix + raw
	exp := expiresAt
	key = &models.APIKey{
		UserID:      userID,
		WorkspaceID: workspaceID,
		Name:        name,
		KeyHash:     hashToken(plaintext),
		KeyPrefix:   plaintext[:prefixLen],
		Scopes:      []string{models.ScopeMigration},
		ExpiresAt:   &exp,
	}
	if err := s.keys.Create(key); err != nil {
		return "", nil, err
	}
	return plaintext, key, nil
}

// CreateEphemeral keys are hidden from the UI and never count against MaxAPIKeys.
func (s *APIKeyService) CreateEphemeral(userID, workspaceID uint, appID *uint, name string, scopes []string, expiresAt time.Time) (plaintext string, key *models.APIKey, err error) {
	normScopes, err := NormalizeScopes(scopes)
	if err != nil {
		return "", nil, err
	}
	raw, _ := generateToken()
	plaintext = keyPrefix + raw
	ws, exp := workspaceID, expiresAt
	key = &models.APIKey{
		UserID:        userID,
		WorkspaceID:   &ws,
		ApplicationID: appID,
		Name:          name,
		KeyHash:       hashToken(plaintext),
		KeyPrefix:     plaintext[:prefixLen],
		Scopes:        normScopes,
		ExpiresAt:     &exp,
		Ephemeral:     true,
	}
	if err := s.keys.Create(key); err != nil {
		return "", nil, err
	}
	return plaintext, key, nil
}

func (s *APIKeyService) Revoke(id uint) error { return s.keys.Revoke(id) }

// SweepExpiredEphemeral cleans up keys orphaned by a runner that died without releasing them.
func (s *APIKeyService) SweepExpiredEphemeral(now time.Time) (int, error) {
	return s.keys.DeleteExpiredEphemeral(now)
}

func (s *APIKeyService) Verify(plaintext string) (*models.APIKey, error) {
	if !strings.HasPrefix(plaintext, keyPrefix) || len(plaintext) < prefixLen {
		return nil, ErrInvalidAPIKey
	}
	candidates, err := s.keys.FindByPrefix(plaintext[:prefixLen])
	if err != nil || len(candidates) == 0 {
		return nil, ErrInvalidAPIKey
	}
	hash := hashToken(plaintext)
	for i := range candidates {
		if subtle.ConstantTimeCompare([]byte(candidates[i].KeyHash), []byte(hash)) != 1 {
			continue
		}
		if !candidates[i].IsValid() {
			return nil, ErrInvalidAPIKey
		}
		_ = s.keys.TouchLastUsed(candidates[i].ID)
		return &candidates[i], nil
	}
	return nil, ErrInvalidAPIKey
}
