// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sealing manages each workspace's sealing key pairs and opens sealed secret values (pkg/sealed).
// Private keys are wrapped under the master KEK and never leave the server except inside a passphrase-sealed
// workspace bundle, so a value in git is readable by Miabi and nobody else.
package sealing

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/pkg/sealed"
	"gorm.io/gorm"
)

// Service is the DB-backed sealing keyring.
type Service struct {
	repo *repositories.WorkspaceSealingKeyRepository
	// createMu serializes first-use key creation so concurrent callers don't both mint version 1.
	createMu sync.Mutex
}

func NewService(repo *repositories.WorkspaceSealingKeyRepository) *Service {
	return &Service{repo: repo}
}

// Active returns the workspace's active sealing key, creating the first one on demand.
func (s *Service) Active(workspaceID uint) (*models.WorkspaceSealingKey, error) {
	k, err := s.repo.FindActive(workspaceID)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if k, err := s.repo.FindActive(workspaceID); err == nil {
		return k, nil
	}
	k, err = s.mint(workspaceID, 1)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Activate(k); err != nil {
		if k2, ferr := s.repo.FindActive(workspaceID); ferr == nil {
			return k2, nil
		}
		return nil, err
	}
	return k, nil
}

// Keys returns every retained version, newest first. It does not create a key.
func (s *Service) Keys(workspaceID uint) ([]models.WorkspaceSealingKey, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// Rotate activates a new key version. Older versions are kept, so values already sealed to them still open.
func (s *Service) Rotate(workspaceID uint) (*models.WorkspaceSealingKey, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	maxVer, err := s.repo.MaxVersion(workspaceID)
	if err != nil {
		return nil, err
	}
	k, err := s.mint(workspaceID, maxVer+1)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Activate(k); err != nil {
		return nil, err
	}
	return k, nil
}

// Unseal opens a sealed value declared on the secret name and reports the key version that opened it. The
// version recorded in the value is tried first, then every other retained key.
func (s *Service) Unseal(workspaceID uint, name, value string) (string, int, error) {
	hint, _, err := sealed.Parse(value)
	if err != nil {
		return "", 0, err
	}
	keys, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return "", 0, err
	}
	if len(keys) == 0 {
		return "", 0, sealed.ErrNoKey
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].Version == hint && keys[j].Version != hint })
	ids := make([]string, len(keys))
	for i := range keys {
		id, err := crypto.Decrypt(keys[i].IdentityEnc)
		if err != nil {
			return "", 0, fmt.Errorf("sealing: unwrap key v%d: %w", keys[i].Version, err)
		}
		ids[i] = id
	}
	plain, used, err := sealed.Open(value, name, ids...)
	if err != nil {
		return "", 0, err
	}
	return plain, keys[used].Version, nil
}

// ExportedKey is a sealing key in the clear, for a passphrase-sealed workspace bundle only.
type ExportedKey struct {
	Version   int       `json:"version"`
	Identity  string    `json:"identity"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

// Export returns the workspace's keys with their private halves unwrapped.
func (s *Service) Export(workspaceID uint) ([]ExportedKey, error) {
	keys, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]ExportedKey, 0, len(keys))
	for i := range keys {
		id, err := crypto.Decrypt(keys[i].IdentityEnc)
		if err != nil {
			return nil, fmt.Errorf("sealing: unwrap key v%d: %w", keys[i].Version, err)
		}
		out = append(out, ExportedKey{Version: keys[i].Version, Identity: id, Active: keys[i].Active, CreatedAt: keys[i].CreatedAt})
	}
	return out, nil
}

// Import restores exported keys so values sealed on the source keep opening here. A workspace without keys
// takes them as they were; one that already has its own keeps its active key and gains the imported ones as
// retired versions. A key already present is skipped. Returns how many were added.
func (s *Service) Import(workspaceID uint, keys []ExportedKey) (int, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	existing, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for i := range existing {
		have[existing[i].PublicKey] = true
	}
	fresh := len(existing) == 0
	next := 0
	if !fresh {
		next = existing[0].Version
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Version < keys[j].Version })
	activeSeen := false
	for i := range keys {
		activeSeen = activeSeen || keys[i].Active
	}
	added := 0
	for i := range keys {
		pub, err := sealed.Recipient(keys[i].Identity)
		if err != nil {
			return added, fmt.Errorf("sealing: imported key v%d: %w", keys[i].Version, err)
		}
		if have[pub] {
			continue
		}
		wrapped, err := crypto.Encrypt(keys[i].Identity)
		if err != nil {
			return added, err
		}
		k := &models.WorkspaceSealingKey{WorkspaceID: workspaceID, PublicKey: pub, IdentityEnc: wrapped, CreatedAt: keys[i].CreatedAt}
		if fresh {
			k.Version = keys[i].Version
			// A bundle with no active flag (hand-edited) still yields one active key: the newest.
			k.Active = keys[i].Active || (!activeSeen && i == len(keys)-1)
		} else {
			next++
			k.Version = next
		}
		if err := s.repo.DB().Create(k).Error; err != nil {
			return added, err
		}
		have[pub] = true
		added++
	}
	return added, nil
}

// ShredWorkspace deletes a workspace's sealing keys, so values sealed to them can never open again.
func (s *Service) ShredWorkspace(workspaceID uint) error {
	return s.repo.DeleteByWorkspace(workspaceID)
}

func (s *Service) mint(workspaceID uint, version int) (*models.WorkspaceSealingKey, error) {
	id, pub, err := sealed.GenerateKey()
	if err != nil {
		return nil, err
	}
	wrapped, err := crypto.Encrypt(id)
	if err != nil {
		return nil, fmt.Errorf("sealing: wrap key: %w", err)
	}
	return &models.WorkspaceSealingKey{WorkspaceID: workspaceID, Version: version, PublicKey: pub, IdentityEnc: wrapped}, nil
}
