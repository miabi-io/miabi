// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backupsettings

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/wsbundle"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const goodPass = "correct-horse-9"

func newSettingsService(t *testing.T) *Service {
	t.Helper()
	crypto.Init("test-encryption-secret")
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.WorkspaceBackupSettings{}); err != nil {
		t.Fatal(err)
	}
	return NewService(repositories.NewWorkspaceBackupSettingsRepository(db))
}

func s3Input() SaveInput {
	secret := "s3-secret"
	return SaveInput{
		S3Enabled:          true,
		S3Bucket:           "backups",
		S3AccessKey:        "key",
		S3SecretKey:        &secret,
		DatabaseBackupPath: "databases",
	}
}

// The destination a workspace's database backups actually get built from must carry
// the passphrase. Assembling a bare Destination beside it is how database backups
// came to be written to object storage in the clear.
func TestDatabaseDestinationCarriesThePassphrase(t *testing.T) {
	svc := newSettingsService(t)
	in := s3Input()
	in.BackupPassphrase = ptr(goodPass)
	if _, err := svc.Save(1, in); err != nil {
		t.Fatalf("save: %v", err)
	}

	dest, err := svc.DatabaseDestination(1)
	if err != nil {
		t.Fatalf("DatabaseDestination: %v", err)
	}
	if dest.Type != "s3" {
		t.Errorf("destination type = %q, want s3", dest.Type)
	}
	if dest.S3 == nil || dest.S3.Path != "databases" {
		t.Errorf("s3 target/prefix not carried through: %+v", dest.S3)
	}
	if dest.GPGPassphrase != goodPass {
		t.Errorf("GPGPassphrase = %q, want the stored passphrase", dest.GPGPassphrase)
	}
}

// A workspace with no S3 target still encrypts: the local backup volume is a
// different destination, not a different policy.
func TestDatabaseDestinationEncryptsLocalBackupsToo(t *testing.T) {
	svc := newSettingsService(t)
	if _, err := svc.Save(1, SaveInput{BackupPassphrase: ptr(goodPass)}); err != nil {
		t.Fatalf("save: %v", err)
	}
	dest, err := svc.DatabaseDestination(1)
	if err != nil {
		t.Fatalf("DatabaseDestination: %v", err)
	}
	if dest.Type != "local" {
		t.Errorf("destination type = %q, want local", dest.Type)
	}
	if dest.GPGPassphrase != goodPass {
		t.Errorf("a local destination dropped the passphrase")
	}
}

// No passphrase is a valid state, not an error — it is what every existing
// workspace has until someone sets one.
func TestDatabaseDestinationWithoutAPassphrase(t *testing.T) {
	svc := newSettingsService(t)
	if _, err := svc.Save(1, s3Input()); err != nil {
		t.Fatalf("save: %v", err)
	}
	dest, err := svc.DatabaseDestination(1)
	if err != nil {
		t.Fatalf("DatabaseDestination: %v", err)
	}
	if dest.GPGPassphrase != "" {
		t.Errorf("GPGPassphrase = %q, want empty", dest.GPGPassphrase)
	}

	// A workspace that has never saved settings at all must not error either.
	if _, err := svc.DatabaseDestination(99); err != nil {
		t.Errorf("unsaved workspace: %v", err)
	}
}

func TestSavePassphraseLifecycle(t *testing.T) {
	svc := newSettingsService(t)
	if _, err := svc.Save(1, s3Input()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("a set passphrase is reported without being returned", func(t *testing.T) {
		in := s3Input()
		in.BackupPassphrase = ptr(goodPass)
		st, err := svc.Save(1, in)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if !st.BackupPassphraseSet {
			t.Error("BackupPassphraseSet is false after setting one")
		}
		if st.BackupPassphraseEnc == goodPass {
			t.Error("the passphrase was stored in the clear")
		}
		got, err := svc.Get(1)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !got.BackupPassphraseSet {
			t.Error("Get did not report the stored passphrase")
		}
	})

	t.Run("nil leaves it unchanged", func(t *testing.T) {
		in := s3Input()
		in.BackupPassphrase = nil
		if _, err := svc.Save(1, in); err != nil {
			t.Fatalf("save: %v", err)
		}
		pass, err := svc.DatabaseBackupPassphrase(1)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if pass != goodPass {
			t.Errorf("passphrase = %q, want it preserved across an unrelated save", pass)
		}
	})

	t.Run("an explicit empty string clears it", func(t *testing.T) {
		in := s3Input()
		in.BackupPassphrase = ptr("")
		st, err := svc.Save(1, in)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if st.BackupPassphraseSet {
			t.Error("BackupPassphraseSet is still true after clearing")
		}
	})

	t.Run("a weak passphrase is refused", func(t *testing.T) {
		in := s3Input()
		in.BackupPassphrase = ptr("short")
		if _, err := svc.Save(1, in); !errors.Is(err, wsbundle.ErrWeakPassphrase) {
			t.Fatalf("error = %v, want ErrWeakPassphrase", err)
		}
	})
}

// A fake rotator, so the settings service can be exercised without the backup
// service and its Docker dependencies.
type fakeRotator struct {
	sealed   int
	rotated  [][2]string
	rotError error
}

func (f *fakeRotator) RewrapSets(_ uint, oldP, newP string) (int, error) {
	if f.rotError != nil {
		return 0, f.rotError
	}
	f.rotated = append(f.rotated, [2]string{oldP, newP})
	return f.sealed, nil
}
func (f *fakeRotator) SealedSetCount(uint) (int, error) { return f.sealed, nil }

// Changing the passphrase must re-seal the recovery points sealed with the old
// one, or they become unreadable the moment the new value is stored.
func TestSavePassphraseRotatesSealedSets(t *testing.T) {
	svc := newSettingsService(t)
	rot := &fakeRotator{sealed: 3}
	svc.SetEnvelopeRotator(rot)

	in := s3Input()
	in.BackupPassphrase = ptr(goodPass)
	if _, err := svc.Save(1, in); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if len(rot.rotated) != 0 {
		t.Error("rotated on the first passphrase, when there was nothing sealed yet")
	}

	const second = "second-passphrase-2"
	in.BackupPassphrase = ptr(second)
	if _, err := svc.Save(1, in); err != nil {
		t.Fatalf("second save: %v", err)
	}
	if len(rot.rotated) != 1 {
		t.Fatalf("rotations = %d, want 1", len(rot.rotated))
	}
	if rot.rotated[0] != [2]string{goodPass, second} {
		t.Errorf("rotated %v, want the old and new passphrase", rot.rotated[0])
	}
}

// If rewrapping fails the old passphrase must remain stored: it still opens every
// set, which is recoverable. Storing the new one would strand them.
func TestSavePassphraseKeepsTheOldOneWhenRotationFails(t *testing.T) {
	svc := newSettingsService(t)
	in := s3Input()
	in.BackupPassphrase = ptr(goodPass)
	if _, err := svc.Save(1, in); err != nil {
		t.Fatal(err)
	}
	svc.SetEnvelopeRotator(&fakeRotator{sealed: 2, rotError: errors.New("nope")})

	in.BackupPassphrase = ptr("another-passphrase-3")
	if _, err := svc.Save(1, in); err == nil {
		t.Fatal("save succeeded despite a failed rotation")
	}
	pass, err := svc.DatabaseBackupPassphrase(1)
	if err != nil {
		t.Fatal(err)
	}
	if pass != goodPass {
		t.Errorf("stored passphrase = %q, want the original kept", pass)
	}
}

// Clearing the passphrase would leave Miabi unable to open sets it still holds.
func TestSaveRefusesToClearWhileSetsAreSealed(t *testing.T) {
	svc := newSettingsService(t)
	svc.SetEnvelopeRotator(&fakeRotator{sealed: 1})
	in := s3Input()
	in.BackupPassphrase = ptr(goodPass)
	if _, err := svc.Save(1, in); err != nil {
		t.Fatal(err)
	}
	in.BackupPassphrase = ptr("")
	if _, err := svc.Save(1, in); !errors.Is(err, ErrSealedSetsExist) {
		t.Fatalf("error = %v, want ErrSealedSetsExist", err)
	}
}

func TestSaveAllowsClearingWithNoSealedSets(t *testing.T) {
	svc := newSettingsService(t)
	svc.SetEnvelopeRotator(&fakeRotator{sealed: 0})
	in := s3Input()
	in.BackupPassphrase = ptr(goodPass)
	if _, err := svc.Save(1, in); err != nil {
		t.Fatal(err)
	}
	in.BackupPassphrase = ptr("")
	st, err := svc.Save(1, in)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if st.BackupPassphraseSet {
		t.Error("the passphrase was not cleared")
	}
}

func ptr(s string) *string { return &s }
