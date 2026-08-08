// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"slices"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
)

// A passphrase supplied through the environment is the operator saying "encrypt with this". Requiring a
// second variable to act on it would leave artifacts in the clear on a deployment that plainly asked
// otherwise — and leave the identity envelope unsealed, which is what makes a fresh-host restore possible.
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

// An explicit variable still wins over the implication, so an operator can
// supply a passphrase for the identity envelope while leaving the dumps
// unencrypted.
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

// Whatever the environment decides must be reported as locked, or the UI offers
// an edit that is silently discarded.
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

// With no environment at all, the stored row is authoritative and nothing is
// locked.
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

// A passphrase is optional. Backing up an unencrypted platform is a legitimate
// choice — a private bucket, a lab, a first run before key custody is arranged —
// and refusing it would mean no backup at all, which is strictly worse.
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

// Tenant artifacts are ENQUEUED by the API server and RUN by the worker, so a composition root that wires
// the tenant source in one process and not the other produces artifacts that queue perfectly and then fail
// with a message about a setting that does not exist. The error must name the real cause.
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
