// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspacekeys

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/keyring"
	"github.com/miabi-io/miabi/internal/services/sealing"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeDEKs struct {
	key   *models.WorkspaceKey
	calls int
	err   error
}

func (f *fakeDEKs) Rotate(_ context.Context, ws uint) (keyring.RotateResult, error) {
	f.calls++
	f.key = &models.WorkspaceKey{WorkspaceID: ws, Version: f.key.Version + 1, RotatedAt: time.Now()}
	return keyring.RotateResult{Version: f.key.Version, Reencrypted: 4}, f.err
}

func (f *fakeDEKs) FindActive(uint) (*models.WorkspaceKey, error) { return f.key, nil }

type fakeCounts map[int]int

func (f fakeCounts) CountSealedByKeyVersion(uint) (map[int]int, error) { return f, nil }

func newService(t *testing.T, deks *fakeDEKs, counts fakeCounts) *Service {
	t.Helper()
	crypto.Init("workspacekeys-test")
	t.Cleanup(func() { crypto.Init("") })
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.WorkspaceSealingKey{}); err != nil {
		t.Fatal(err)
	}
	return NewService(deks, deks, sealing.NewService(repositories.NewWorkspaceSealingKeyRepository(db)), counts)
}

func TestFirstRotationIsAllowedThenBlockedForSixMonths(t *testing.T) {
	deks := &fakeDEKs{key: &models.WorkspaceKey{Version: 1, RotatedAt: time.Now().AddDate(-2, 0, 0)}}
	s := newService(t, deks, fakeCounts{})

	st, err := s.Status(1)
	if err != nil || !st.CanRotate || st.LastRotatedAt != nil {
		t.Fatalf("a never-rotated workspace must be rotatable: %+v, %v", st, err)
	}
	res, err := s.Rotate(context.Background(), 1)
	if err != nil || res.DataKeyVersion != 2 || res.SealingKeyVersion != 2 {
		t.Fatalf("Rotate = %+v, %v", res, err)
	}

	_, err = s.Rotate(context.Background(), 1)
	var tooSoon *TooSoonError
	if !errors.As(err, &tooSoon) {
		t.Fatalf("second rotation must be refused, got %v", err)
	}
	if deks.calls != 1 {
		t.Fatalf("data key rotated %d times, want 1", deks.calls)
	}

	s.now = func() time.Time { return time.Now().AddDate(0, RotationMonths, 1) }
	if st, _ := s.Status(1); !st.CanRotate {
		t.Fatal("rotation must be allowed again after the interval")
	}
}

func TestAutoRotatedDataKeyCountsAsARotation(t *testing.T) {
	deks := &fakeDEKs{key: &models.WorkspaceKey{Version: 3, RotatedAt: time.Now().AddDate(0, -1, 0)}}
	s := newService(t, deks, fakeCounts{})
	st, err := s.Status(1)
	if err != nil || st.CanRotate || st.NextRotationAt == nil {
		t.Fatalf("a key rotated last month must block rotation: %+v, %v", st, err)
	}
}

func TestStaleCiphertextStillRotatesTheSealingKey(t *testing.T) {
	deks := &fakeDEKs{key: &models.WorkspaceKey{Version: 1}, err: keyring.ErrStaleCiphertext}
	s := newService(t, deks, fakeCounts{})
	res, err := s.Rotate(context.Background(), 1)
	if err != nil || res.SealingKeyVersion != 2 {
		t.Fatalf("Rotate = %+v, %v", res, err)
	}
}

func TestStatusCountsOutdatedSealedSecrets(t *testing.T) {
	deks := &fakeDEKs{key: &models.WorkspaceKey{Version: 1}}
	s := newService(t, deks, fakeCounts{1: 3, 2: 2})
	if _, err := s.Rotate(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(1)
	if err != nil {
		t.Fatal(err)
	}
	if st.SealedSecrets != 5 || st.OutdatedSealedSecrets != 3 {
		t.Fatalf("sealed = %d, outdated = %d; want 5, 3", st.SealedSecrets, st.OutdatedSealedSecrets)
	}
	if len(st.SealingKeys) != 2 || !st.SealingKeys[0].Active || st.SealingKeys[0].Version != 2 {
		t.Fatalf("keys = %+v", st.SealingKeys)
	}
}
