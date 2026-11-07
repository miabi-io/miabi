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

// Package keyring manages per-workspace DEKs wrapped by the master KEK, which never leaves the crypto package.
package keyring

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// Reencryptor is implemented by each owner of workspace ciphertext; idempotent.
type Reencryptor interface {
	Reencrypt(ctx context.Context, workspaceID uint) (int, error)
}

// RotateResult summarizes a rotation.
type RotateResult struct {
	Version     int `json:"version"`
	Reencrypted int `json:"reencrypted"`
	// StaleColumns lists the "table.column" locations that kept old key versions alive (ErrStaleCiphertext).
	StaleColumns []string `json:"stale_columns,omitempty"`
}

const dekSize = 32 // AES-256

// ErrStaleCiphertext: old key versions were kept and data stays readable; the listed columns need a Reencryptor.
var ErrStaleCiphertext = errors.New("keyring: old key versions kept, ciphertext still references them")

// Service is the DB-backed keyring.
type Service struct {
	repo *repositories.WorkspaceKeyRepository

	mu    sync.RWMutex
	cache map[cacheKey][]byte // (workspace, version) -> unwrapped DEK
	// Serializes first-use DEK creation so concurrent writers don't create duplicate keys.
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

// Rotate keeps old versions on a partial sweep so unswept ciphertext stays decryptable.
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
	res := RotateResult{Version: next}
	res.Reencrypted, err = s.sweep(ctx, workspaceID)
	if err == nil {
		res.StaleColumns, err = s.retireOldVersions(workspaceID, next)
	}
	return res, err
}

// A sweep only covers registered owners, so the database is checked rather than trusted.
func (s *Service) retireOldVersions(workspaceID uint, active int) ([]string, error) {
	stale, err := s.repo.StaleCiphertext(workspaceID, active)
	if err != nil {
		return nil, fmt.Errorf("keyring: verify rotation sweep: %w", err)
	}
	if len(stale) > 0 {
		return stale, fmt.Errorf("%w: %s", ErrStaleCiphertext, strings.Join(stale, ", "))
	}
	if err := s.repo.DeleteOldVersions(workspaceID, active); err != nil {
		logger.Warn("keyring: prune old key versions failed", "workspace", workspaceID, "error", err)
		return nil, nil
	}
	s.mu.Lock()
	for k := range s.cache {
		if k.ws == workspaceID && k.ver != active {
			delete(s.cache, k)
		}
	}
	s.mu.Unlock()
	return nil, nil
}

// Migrate moves legacy master-key data to the workspace DEK without creating a new version.
func (s *Service) Migrate(ctx context.Context, workspaceID uint) (int, error) {
	if _, _, err := s.ActiveDEK(workspaceID); err != nil { // creates v1 if none
		return 0, err
	}
	return s.sweep(ctx, workspaceID)
}

// One reencryptor's error doesn't abort the others; old versions stay, so data remains safe.
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

// ShredWorkspace deletes a workspace's keys, making its ciphertext unrecoverable.
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
