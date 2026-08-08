// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package keyring manages per-workspace data-encryption keys. Each DEK is stored wrapped by the master KEK
// and unwrapped into an in-memory cache on demand. It implements crypto.Keyring so the crypto package can
// resolve workspace keys without importing it; the master KEK never leaves the crypto package.
package keyring

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// Reencryptor re-encrypts a workspace's stored secrets under its current active DEK. Implemented by each
// service that owns workspace-scoped ciphertext and registered with the keyring; the rotation sweep calls
// every registered one. Idempotent, and returns the number of values rewritten.
type Reencryptor interface {
	Reencrypt(ctx context.Context, workspaceID uint) (int, error)
}

// RotateResult summarizes a rotation.
type RotateResult struct {
	Version     int `json:"version"`
	Reencrypted int `json:"reencrypted"`
}

const dekSize = 32 // AES-256

// Service is the DB-backed keyring.
type Service struct {
	repo *repositories.WorkspaceKeyRepository

	mu    sync.RWMutex
	cache map[cacheKey][]byte // (workspace, version) -> unwrapped DEK
	// createMu serializes first-use DEK creation per process so two concurrent
	// writers to a new workspace don't race to create duplicate keys.
	createMu sync.Mutex

	reencryptors []Reencryptor
}

// Register adds a Reencryptor consulted by the rotation sweep. Call at startup.
func (s *Service) Register(r Reencryptor) { s.reencryptors = append(s.reencryptors, r) }

type cacheKey struct {
	ws  uint
	ver int
}

func NewService(repo *repositories.WorkspaceKeyRepository) *Service {
	return &Service{repo: repo, cache: map[cacheKey][]byte{}}
}

// ActiveDEK returns the workspace's active key version + DEK, creating the first
// key on demand. Implements crypto.Keyring.
func (s *Service) ActiveDEK(workspaceID uint) (int, []byte, error) {
	wk, err := s.repo.FindActive(workspaceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		wk, err = s.createFirst(workspaceID)
	}
	if err != nil {
		return 0, nil, err
	}
	dek, err := s.unwrap(wk)
	if err != nil {
		return 0, nil, err
	}
	return wk.Version, dek, nil
}

// DEK returns a specific key version's DEK (to decrypt older ciphertext).
// Implements crypto.Keyring.
func (s *Service) DEK(workspaceID uint, version int) ([]byte, error) {
	ck := cacheKey{ws: workspaceID, ver: version}
	s.mu.RLock()
	if dek, ok := s.cache[ck]; ok {
		s.mu.RUnlock()
		return dek, nil
	}
	s.mu.RUnlock()
	wk, err := s.repo.FindVersion(workspaceID, version)
	if err != nil {
		return nil, fmt.Errorf("keyring: workspace %d key v%d: %w", workspaceID, version, err)
	}
	return s.unwrap(wk)
}

// createFirst creates and persists a workspace's first (version 1) active DEK.
func (s *Service) createFirst(workspaceID uint) (*models.WorkspaceKey, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	// Re-check under the lock: another goroutine may have created it.
	if wk, err := s.repo.FindActive(workspaceID); err == nil {
		return wk, nil
	}
	wk, err := s.generate(workspaceID, 1, true)
	if err != nil {
		return nil, err
	}
	wk.RotatedAt = time.Now()
	if err := s.repo.Create(wk); err != nil {
		// Lost a cross-process race? fall back to whatever is now active.
		if wk2, ferr := s.repo.FindActive(workspaceID); ferr == nil {
			return wk2, nil
		}
		return nil, err
	}
	return wk, nil
}

// generate makes a fresh random DEK wrapped under the master KEK.
func (s *Service) generate(workspaceID uint, version int, active bool) (*models.WorkspaceKey, error) {
	raw := make([]byte, dekSize)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return nil, err
	}
	wrapped, err := crypto.Encrypt(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		return nil, fmt.Errorf("keyring: wrap DEK: %w", err)
	}
	return &models.WorkspaceKey{
		WorkspaceID: workspaceID, Version: version, WrappedDEK: wrapped, Active: active,
	}, nil
}

// Rotate creates a new active DEK version for the workspace and re-encrypts all registered owners' data to
// it. On a partial sweep old versions are RETAINED but deactivated, so not-yet-swept ciphertext stays
// decryptable — rotation is always safe. A full sweep retires them.
func (s *Service) Rotate(ctx context.Context, workspaceID uint) (RotateResult, error) {
	next, err := s.repo.MaxVersion(workspaceID)
	if err != nil {
		return RotateResult{}, err
	}
	next++
	wk, err := s.generate(workspaceID, next, true)
	if err != nil {
		return RotateResult{}, err
	}
	wk.RotatedAt = time.Now()
	if err := s.repo.DB().Transaction(func(tx *gorm.DB) error {
		if derr := s.repo.DeactivateAll(tx, workspaceID); derr != nil {
			return derr
		}
		return tx.Create(wk).Error
	}); err != nil {
		return RotateResult{}, err
	}
	// New writes now use `next` (FindActive returns it). Re-encrypt existing data.
	total, err := s.sweep(ctx, workspaceID)
	if err == nil {
		// Full sweep succeeded: every value is on the new version, so the old DEKs
		// are unreferenced and can be retired. On partial failure we keep them so
		// un-swept ciphertext stays decryptable.
		if perr := s.repo.DeleteOldVersions(workspaceID, next); perr != nil {
			logger.Warn("keyring: prune old key versions failed", "workspace", workspaceID, "error", perr)
		} else {
			s.mu.Lock()
			for k := range s.cache {
				if k.ws == workspaceID && k.ver != next {
					delete(s.cache, k)
				}
			}
			s.mu.Unlock()
		}
	}
	return RotateResult{Version: next, Reencrypted: total}, err
}

// Migrate ensures the workspace has an active DEK and re-encrypts existing legacy
// (master-key) data to it — without creating a new version. Used for the one-time
// migration off the global key.
func (s *Service) Migrate(ctx context.Context, workspaceID uint) (int, error) {
	if _, _, err := s.ActiveDEK(workspaceID); err != nil { // creates v1 if none
		return 0, err
	}
	return s.sweep(ctx, workspaceID)
}

// sweep runs every registered reencryptor for the workspace. A reencryptor error is logged and aggregated
// but does not abort the others; the first error is returned so the caller knows coverage was partial (old
// versions stay, so data remains safe).
func (s *Service) sweep(ctx context.Context, workspaceID uint) (int, error) {
	total := 0
	var firstErr error
	for _, r := range s.reencryptors {
		n, err := r.Reencrypt(ctx, workspaceID)
		total += n
		if err != nil {
			logger.Warn("keyring: reencrypt failed", "workspace", workspaceID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return total, firstErr
}

// AutoRotateDue rotates every workspace whose active key is older than `older`.
// Driven by the auto-rotate cron (off by default).
func (s *Service) AutoRotateDue(ctx context.Context, older time.Duration) error {
	keys, err := s.repo.ListActiveOlderThan(time.Now().Add(-older))
	if err != nil {
		return err
	}
	for i := range keys {
		ws := keys[i].WorkspaceID
		if res, rerr := s.Rotate(ctx, ws); rerr != nil {
			logger.Warn("keyring: auto-rotate failed", "workspace", ws, "error", rerr)
		} else {
			logger.Info("keyring: auto-rotated workspace key", "workspace", ws, "version", res.Version, "reencrypted", res.Reencrypted)
		}
	}
	return nil
}

// ShredWorkspace permanently deletes a workspace's keys (DB + in-memory cache),
// rendering its ciphertext unrecoverable. Called on workspace delete.
func (s *Service) ShredWorkspace(workspaceID uint) error {
	s.mu.Lock()
	for k := range s.cache {
		if k.ws == workspaceID {
			delete(s.cache, k)
		}
	}
	s.mu.Unlock()
	return s.repo.DeleteByWorkspace(workspaceID)
}

// unwrap decrypts a wrapped DEK with the master KEK and caches it.
func (s *Service) unwrap(wk *models.WorkspaceKey) ([]byte, error) {
	ck := cacheKey{ws: wk.WorkspaceID, ver: wk.Version}
	s.mu.RLock()
	if dek, ok := s.cache[ck]; ok {
		s.mu.RUnlock()
		return dek, nil
	}
	s.mu.RUnlock()
	b64, err := crypto.Decrypt(wk.WrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("keyring: unwrap DEK: %w", err)
	}
	dek, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("keyring: decode DEK: %w", err)
	}
	s.mu.Lock()
	s.cache[ck] = dek
	s.mu.Unlock()
	return dek, nil
}
