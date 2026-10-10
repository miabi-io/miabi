// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package workspacekeys is the workspace-facing view of a workspace's keys: its data-encryption key and its
// sealing key. A workspace owner or admin rotates both together, at most once per RotationInterval.
package workspacekeys

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/keyring"
	"github.com/miabi-io/miabi/internal/services/sealing"
	"gorm.io/gorm"
)

// RotationMonths is the minimum time between two rotations of a workspace's keys.
const RotationMonths = 6

// ErrUnavailable means per-workspace data keys are not enabled on this instance.
var ErrUnavailable = errors.New("per-workspace encryption keys are not enabled on this instance")

// TooSoonError refuses a rotation inside the interval.
type TooSoonError struct{ Next time.Time }

func (e *TooSoonError) Error() string {
	return fmt.Sprintf("workspace keys were rotated less than %d months ago; next rotation is possible on %s",
		RotationMonths, e.Next.UTC().Format("2006-01-02"))
}

// DEKs is the slice of the keyring this package needs.
type DEKs interface {
	Rotate(ctx context.Context, workspaceID uint) (keyring.RotateResult, error)
}

// ActiveDEKFinder reads the workspace's active data key.
type ActiveDEKFinder interface {
	FindActive(workspaceID uint) (*models.WorkspaceKey, error)
}

// SealedCounter counts secrets set from sealed values, by sealing key version.
type SealedCounter interface {
	CountSealedByKeyVersion(workspaceID uint) (map[int]int, error)
}

type Service struct {
	deks    DEKs
	active  ActiveDEKFinder
	sealing *sealing.Service
	secrets SealedCounter
	now     func() time.Time
	// mu stops two rotations of one workspace racing past the interval check in this process.
	mu sync.Mutex
}

func NewService(deks DEKs, active ActiveDEKFinder, sealingSvc *sealing.Service, secrets SealedCounter) *Service {
	return &Service{deks: deks, active: active, sealing: sealingSvc, secrets: secrets, now: time.Now}
}

// SealingKey describes one sealing key version.
type SealingKey struct {
	Version   int       `json:"version"`
	PublicKey string    `json:"public_key"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	// SealedSecrets is how many secrets were last set from a value sealed to this version.
	SealedSecrets int `json:"sealed_secrets"`
}

// Status is a workspace's key posture.
type Status struct {
	EncryptionEnabled bool `json:"encryption_enabled"`
	DataKeyVersion    int  `json:"data_key_version"`
	// LastRotatedAt is the last rotation of either key; nil until the first one.
	LastRotatedAt *time.Time `json:"last_rotated_at,omitempty"`
	// NextRotationAt is when a rotation is next allowed; nil when it is allowed now.
	NextRotationAt *time.Time   `json:"next_rotation_at,omitempty"`
	CanRotate      bool         `json:"can_rotate"`
	RotationMonths int          `json:"rotation_months"`
	SealingKeys    []SealingKey `json:"sealing_keys"`
	SealedSecrets  int          `json:"sealed_secrets"`
	// OutdatedSealedSecrets were sealed to a key that is no longer active; they still open, and re-sealing
	// them moves them to the active key.
	OutdatedSealedSecrets int `json:"outdated_sealed_secrets"`
}

// RotateResult is the outcome of a rotation.
type RotateResult struct {
	DataKeyVersion    int      `json:"data_key_version"`
	Reencrypted       int      `json:"reencrypted"`
	StaleColumns      []string `json:"stale_columns,omitempty"`
	SealingKeyVersion int      `json:"sealing_key_version"`
}

// Status reports the workspace's keys, creating the first sealing key if it has none.
func (s *Service) Status(workspaceID uint) (*Status, error) {
	if _, err := s.sealing.Active(workspaceID); err != nil {
		return nil, err
	}
	keys, err := s.sealing.Keys(workspaceID)
	if err != nil {
		return nil, err
	}
	counts, err := s.secrets.CountSealedByKeyVersion(workspaceID)
	if err != nil {
		return nil, err
	}
	st := &Status{EncryptionEnabled: crypto.Enabled(), RotationMonths: RotationMonths, SealingKeys: make([]SealingKey, 0, len(keys))}
	var last time.Time
	for i := range keys {
		k := keys[i]
		n := counts[k.Version]
		st.SealingKeys = append(st.SealingKeys, SealingKey{Version: k.Version, PublicKey: k.PublicKey, Active: k.Active, CreatedAt: k.CreatedAt, SealedSecrets: n})
		st.SealedSecrets += n
		if !k.Active {
			st.OutdatedSealedSecrets += n
		}
		if k.Active && k.Version > 1 && k.CreatedAt.After(last) {
			last = k.CreatedAt
		}
	}
	sort.SliceStable(st.SealingKeys, func(i, j int) bool { return st.SealingKeys[i].Version > st.SealingKeys[j].Version })

	dek, err := s.active.FindActive(workspaceID)
	switch {
	case err == nil:
		st.DataKeyVersion = dek.Version
		// Version 1's timestamp is its creation, not a rotation.
		if dek.Version > 1 && dek.RotatedAt.After(last) {
			last = dek.RotatedAt
		}
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}

	st.CanRotate = s.deks != nil
	if !last.IsZero() {
		st.LastRotatedAt = &last
		if next := last.AddDate(0, RotationMonths, 0); s.now().Before(next) {
			st.NextRotationAt = &next
			st.CanRotate = false
		}
	}
	return st, nil
}

// Rotate rotates the workspace's data key (re-encrypting what it protects) and its sealing key. It refuses
// with *TooSoonError inside the interval. Values sealed to the previous sealing key keep opening.
func (s *Service) Rotate(ctx context.Context, workspaceID uint) (*RotateResult, error) {
	if s.deks == nil {
		return nil, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.Status(workspaceID)
	if err != nil {
		return nil, err
	}
	if !st.CanRotate {
		return nil, &TooSoonError{Next: *st.NextRotationAt}
	}
	res := &RotateResult{}
	dek, err := s.deks.Rotate(ctx, workspaceID)
	// Stale columns mean old data-key versions were kept: the rotation itself succeeded.
	if err != nil && !errors.Is(err, keyring.ErrStaleCiphertext) {
		return nil, err
	}
	res.DataKeyVersion, res.Reencrypted, res.StaleColumns = dek.Version, dek.Reencrypted, dek.StaleColumns
	k, err := s.sealing.Rotate(workspaceID)
	if err != nil {
		return res, fmt.Errorf("data key rotated to v%d, but the sealing key was not: %w", dek.Version, err)
	}
	res.SealingKeyVersion = k.Version
	return res, nil
}
