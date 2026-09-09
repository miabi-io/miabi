// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/dbenvelope"
	"github.com/miabi-io/miabi/internal/models"
)

func sealedSet(t *testing.T) (*models.DatabaseBackupSet, string) {
	t.Helper()
	key, err := dbenvelope.NewDataKey()
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbenvelope.Seal(key, "a-recovery-passphrase-1")
	if err != nil {
		t.Fatal(err)
	}
	return &models.DatabaseBackupSet{
		Ref: "mbdb_pg_20260909T030000Z", Engine: models.DBEnginePostgres, Version: "17",
		Encrypted: true, S3Bucket: "acme-backups", S3Path: "databases", Envelope: env,
		CreatedAt: time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC),
		Items: []models.Backup{
			{Filename: "orders_20260909.sql.gz.gpg", Encrypted: true},
			{Filename: "billing_20260909.sql.gz.gpg", Encrypted: true},
		},
	}, key
}

// The kit is what someone reads when Miabi is gone, so it has to carry the
// envelope, where the artifacts are, and how to open them.
func TestRecoveryKitCarriesWhatRecoveryNeeds(t *testing.T) {
	set, _ := sealedSet(t)
	kit := renderRecoveryKit(set, time.Now().UTC())

	for _, want := range []string{
		set.Ref,
		set.Envelope,
		"acme-backups",
		"orders_20260909.sql.gz.gpg",
		"billing_20260909.sql.gz.gpg",
		"argon2id",
		"AES-256-GCM",
		"gpg --batch",
	} {
		if !strings.Contains(kit, want) {
			t.Errorf("the kit does not mention %q", want)
		}
	}
}

// A kit is safe to store beside the backups precisely because it cannot open them
// on its own. Leaking the data key into it would undo the encryption entirely.
func TestRecoveryKitNeverContainsTheDataKey(t *testing.T) {
	set, key := sealedSet(t)
	if strings.Contains(renderRecoveryKit(set, time.Now().UTC()), key) {
		t.Fatal("the recovery kit contains the data key in the clear")
	}
}

// The embedded script must describe the format this build actually seals with, or
// it decrypts nothing.
func TestRecoveryKitScriptMatchesTheEnvelopeFormat(t *testing.T) {
	f := dbenvelope.Format()
	script := kitPythonScript(f)
	for _, want := range []string{
		fmt.Sprintf("b%q", f.Magic),
		fmt.Sprintf("time_cost=%d", f.ArgonTime),
		fmt.Sprintf("memory_cost=%d", f.ArgonMemoryKB),
		fmt.Sprintf("parallelism=%d", f.ArgonLanes),
		fmt.Sprintf("hash_len=%d", f.KeyLen),
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not carry %q", want)
		}
	}
	// Prompts must not land on stdout, or redirecting the output captures them
	// along with the key.
	if !strings.Contains(script, "stream=sys.stderr") {
		t.Error("the passphrase prompt is not sent to stderr")
	}
}

// An unencrypted set still gets a kit — it just has nothing to unseal.
func TestRecoveryKitForAnUnencryptedSet(t *testing.T) {
	set := &models.DatabaseBackupSet{
		Ref: "mbdb_pg_20260101T000000Z", Engine: models.DBEnginePostgres,
		S3Bucket: "acme-backups", Items: []models.Backup{{Filename: "orders.sql.gz"}},
	}
	kit := renderRecoveryKit(set, time.Now().UTC())
	if !strings.Contains(kit, "Not encrypted") {
		t.Error("an unencrypted set's kit does not say so")
	}
	if strings.Contains(kit, "argon2id") {
		t.Error("an unencrypted set's kit describes an envelope it does not have")
	}
}

func TestRecoveryKitFilename(t *testing.T) {
	set, _ := sealedSet(t)
	name, body, err := (&Service{}).RecoveryKit(set)
	if err != nil {
		t.Fatal(err)
	}
	if name != set.Ref+"-recovery-kit.md" {
		t.Errorf("filename = %q", name)
	}
	if len(body) == 0 {
		t.Error("empty kit")
	}
}
