// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package sealing

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/pkg/sealed"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newService(t *testing.T) *Service {
	t.Helper()
	crypto.Init("sealing-test-master-key")
	t.Cleanup(func() { crypto.Init("") })
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.WorkspaceSealingKey{}); err != nil {
		t.Fatal(err)
	}
	return NewService(repositories.NewWorkspaceSealingKeyRepository(db))
}

func TestActiveCreatesOnceAndWrapsThePrivateKey(t *testing.T) {
	s := newService(t)
	a, err := s.Active(1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Active(1)
	if a.Version != 1 || b.PublicKey != a.PublicKey {
		t.Fatalf("want one v1 key, got v%d / %q vs %q", a.Version, a.PublicKey, b.PublicKey)
	}
	if !crypto.IsEncrypted(a.IdentityEnc) {
		t.Fatal("private key must be stored wrapped under the master key")
	}
}

func TestRotateKeepsOldValuesOpening(t *testing.T) {
	s := newService(t)
	v1, _ := s.Active(7)
	old, _ := sealed.Seal(v1.PublicKey, v1.Version, "db", "pw-1")

	v2, err := s.Rotate(7)
	if err != nil || v2.Version != 2 {
		t.Fatalf("Rotate = %v, %v", v2, err)
	}
	cur, _ := s.Active(7)
	if cur.Version != 2 {
		t.Fatalf("active = v%d, want v2", cur.Version)
	}
	got, ver, err := s.Unseal(7, "db", old)
	if err != nil || got != "pw-1" || ver != 1 {
		t.Fatalf("Unseal old = %q, v%d, %v", got, ver, err)
	}
	fresh, _ := sealed.Seal(v2.PublicKey, 0, "db", "pw-2")
	if got, ver, err := s.Unseal(7, "db", fresh); err != nil || got != "pw-2" || ver != 2 {
		t.Fatalf("Unseal new = %q, v%d, %v", got, ver, err)
	}
}

func TestUnsealRefusesAnotherWorkspace(t *testing.T) {
	s := newService(t)
	a, _ := s.Active(1)
	_, _ = s.Active(2)
	v, _ := sealed.Seal(a.PublicKey, a.Version, "db", "pw")
	if _, _, err := s.Unseal(2, "db", v); !errors.Is(err, sealed.ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
}

func TestImportCarriesKeysToAnotherWorkspace(t *testing.T) {
	s := newService(t)
	src, _ := s.Active(1)
	_, _ = s.Rotate(1)
	v, _ := sealed.Seal(src.PublicKey, src.Version, "db", "pw")
	exported, err := s.Export(1)
	if err != nil || len(exported) != 2 {
		t.Fatalf("Export = %d keys, %v", len(exported), err)
	}

	n, err := s.Import(3, exported)
	if err != nil || n != 2 {
		t.Fatalf("Import into empty = %d, %v", n, err)
	}
	if got, _, err := s.Unseal(3, "db", v); err != nil || got != "pw" {
		t.Fatalf("Unseal after import = %q, %v", got, err)
	}
	if n, _ := s.Import(3, exported); n != 0 {
		t.Fatalf("re-import added %d keys, want 0", n)
	}

	own, _ := s.Active(4)
	if _, err := s.Import(4, exported); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Active(4); cur.PublicKey != own.PublicKey {
		t.Fatal("import into a workspace with keys must keep its active key")
	}
	if got, _, err := s.Unseal(4, "db", v); err != nil || got != "pw" {
		t.Fatalf("Unseal after merge = %q, %v", got, err)
	}
}
