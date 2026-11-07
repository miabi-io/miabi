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

package platformbackup

import (
	"slices"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
)

// An env passphrase means "encrypt with this", and also seals the identity envelope for fresh-host restores.
func TestPassphraseImpliesEncryptionAndIdentity(t *testing.T) {
	s := &Service{env: config.PlatformBackupConfig{Passphrase: "correct-horse-9!"}}
	st := &models.PlatformBackupSettings{}
	s.applyEnv(st)

	if !st.EncryptBackups {
		t.Error("a passphrase in the environment did not turn on artifact encryption")
	}
	if !st.IncludeIdentity {
		t.Error("a passphrase in the environment did not seal the identity envelope")
	}
	if st.BackupPassphraseEnc != envSecretMarker {
		t.Errorf("BackupPassphraseEnc = %q, want the env marker", st.BackupPassphraseEnc)
	}
	if got, err := s.passphrase(st); err != nil || got != "correct-horse-9!" {
		t.Errorf("passphrase() = %q, %v", got, err)
	}
}

// An explicit variable wins, so the envelope can use a passphrase while dumps stay unencrypted.
func TestExplicitEncryptOverridesTheImplication(t *testing.T) {
	s := &Service{env: config.PlatformBackupConfig{
		Passphrase: "correct-horse-9!",
		Encrypt:    false,
		EncryptSet: true,
	}}
	st := &models.PlatformBackupSettings{}
	s.applyEnv(st)

	if st.EncryptBackups {
		t.Error("MIABI_PLATFORM_BACKUP_ENCRYPT=false was overridden by the passphrase implication")
	}
	if !st.IncludeIdentity {
		t.Error("the identity envelope should still be sealed")
	}
}

// Env-decided settings must be reported locked, or the UI offers an edit that is silently discarded.
func TestEnvLockedReportsImpliedEncryption(t *testing.T) {
	s := &Service{env: config.PlatformBackupConfig{Passphrase: "correct-horse-9!"}}
	locked := s.envLockedFields()

	for _, field := range []string{"backup_passphrase", "encrypt_backups"} {
		if !slices.Contains(locked, field) {
			t.Errorf("%q is env-controlled but not reported as locked: %v", field, locked)
		}
	}
	// Nothing else was configured, so nothing else may claim to be locked.
	if slices.Contains(locked, "s3_bucket") {
		t.Errorf("s3_bucket reported locked with no S3 environment: %v", locked)
	}
}

func TestNoEnvLeavesSettingsAlone(t *testing.T) {
	s := &Service{}
	st := &models.PlatformBackupSettings{S3Bucket: "from-the-ui", EncryptBackups: false}
	s.applyEnv(st)

	if st.S3Bucket != "from-the-ui" || st.EncryptBackups {
		t.Errorf("an empty environment changed the stored settings: %+v", st)
	}
	if got := s.envLockedFields(); len(got) != 0 {
		t.Errorf("envLockedFields() = %v, want none", got)
	}
}

// Unencrypted backups are legitimate; refusing them would mean no backup at all.
func TestNoPassphraseLeavesEncryptionOff(t *testing.T) {
	s := &Service{env: config.PlatformBackupConfig{
		S3Bucket:    "b",
		S3AccessKey: "k",
		S3SecretKey: "s",
	}}
	st := &models.PlatformBackupSettings{}
	s.applyEnv(st)

	if st.EncryptBackups {
		t.Error("encryption was turned on with no passphrase to encrypt with")
	}
	if st.IncludeIdentity {
		t.Error("the identity envelope was requested with nothing to seal it with")
	}
	if st.BackupPassphraseEnc != "" {
		t.Errorf("BackupPassphraseEnc = %q, want empty", st.BackupPassphraseEnc)
	}
	// The S3 target still applies: a passphrase and a destination are unrelated.
	if !st.S3Enabled || st.S3Bucket != "b" {
		t.Errorf("the S3 target was not applied: %+v", st)
	}
	// And nothing passphrase-related may claim to be env-locked.
	for _, field := range s.envLockedFields() {
		if field == "backup_passphrase" || field == "encrypt_backups" {
			t.Errorf("%q reported locked with no passphrase in the environment", field)
		}
	}
}

// Enqueued by the API server but run by the worker, so a missing tenant source must be reported as a wiring fault.
func TestTenantSourceMissingIsReportedAsAWiringFault(t *testing.T) {
	s := &Service{} // no tenant source, as an unwired worker would be

	if s.TenantCaptureAvailable() {
		t.Fatal("TenantCaptureAvailable() is true with no source wired")
	}

	_, err := s.resolveTenantDatabase(&models.PlatformBackup{WorkspaceSlug: "ws", DatabaseName: "db"})
	if err == nil {
		t.Fatal("resolving a tenant database with no source succeeded")
	}
	for _, want := range []string{"worker", "wiring"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q so the cause is findable: %v", want, err)
		}
	}

	if _, err := s.resolveTenantVolume(&models.PlatformBackup{VolumeName: "v"}); err == nil {
		t.Fatal("resolving a tenant volume with no source succeeded")
	}
}
