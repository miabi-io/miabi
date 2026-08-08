// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// keyPrefix is the human-readable namespace prefix on every token. Tokens have
// the form mb_<64-hex-secret>; the stored KeyPrefix is keyPrefix + the first 8
// hex chars, used for an indexed lookup before the constant-time hash compare.
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

// NormalizeScopes validates, deduplicates, and defaults a requested scope set.
// An empty set defaults to read-only; an unknown scope is an error.
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

// Create generates a new API key, persists its hash, and returns the one-time
// plaintext token (shown to the user only once).
func (s *APIKeyService) Create(userID uint, workspaceID *uint, name string, allowedIPs, scopes []string, expiresAt *time.Time) (plaintext string, key *models.APIKey, err error) {
	normScopes, err := NormalizeScopes(scopes)
	if err != nil {
		return "", nil, err
	}
	// Quota is enforced for workspace-scoped keys; personal keys (no workspace)
	// are not counted against a workspace plan.
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

// CreateEphemeral mints a short-lived, machine-minted API key for a runner job: workspace-scoped, bound
// to one application, expiring at the job deadline, and marked Ephemeral so it is hidden from the UI and
// excluded from the MaxAPIKeys quota. No quota check — job credentials never count against a plan.
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

// Revoke marks a key revoked (used to kill a run's ephemeral credentials the
// moment the run reaches a terminal state).
func (s *APIKeyService) Revoke(id uint) error { return s.keys.Revoke(id) }

// SweepExpiredEphemeral deletes ephemeral job keys past their expiry — a
// belt-and-suspenders cleanup for keys orphaned by a runner that died without
// its run releasing them. Returns the number deleted.
func (s *APIKeyService) SweepExpiredEphemeral(now time.Time) (int, error) {
	return s.keys.DeleteExpiredEphemeral(now)
}

// Verify validates a presented token and returns the matching key. The lookup
// is indexed on the prefix; the secret is compared in constant time, and the
// key must be neither revoked nor expired.
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
