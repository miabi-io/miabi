// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/dbenvelope"
	"github.com/miabi-io/miabi/internal/models"
)

const verifyPass = "a-verification-passphrase-1"

func setWithItems(t *testing.T, sealed bool) *models.DatabaseBackupSet {
	t.Helper()
	set := &models.DatabaseBackupSet{
		Ref: "mbdb_pg_1", S3Path: "backups/pg/mbdb_pg_1",
		Items: []models.Backup{
			{Filename: "orders.sql.gz.gpg", SizeBytes: 100},
			{Filename: "billing.sql.gz.gpg", SizeBytes: 200},
		},
	}
	if sealed {
		key, err := dbenvelope.NewDataKey()
		if err != nil {
			t.Fatal(err)
		}
		if set.Envelope, err = dbenvelope.Seal(key, verifyPass); err != nil {
			t.Fatal(err)
		}
	}
	return set
}

func intactBucket() map[string]int64 {
	return map[string]int64{"orders.sql.gz.gpg": 100, "billing.sql.gz.gpg": 200}
}

func TestVerifyPassesOnAnIntactSet(t *testing.T) {
	res := checkSetAgainstBucket(setWithItems(t, true), intactBucket(), verifyPass)
	if !res.OK || res.Error != "" {
		t.Errorf("OK=%v error=%q, want a clean pass", res.OK, res.Error)
	}
	if res.Checked != 2 || !res.EnvelopeOK {
		t.Errorf("checked=%d envelopeOK=%v", res.Checked, res.EnvelopeOK)
	}
}

// The common real failure: a lifecycle rule removed the objects while the history
// still lists them.
func TestVerifyNamesMissingArtifacts(t *testing.T) {
	bucket := intactBucket()
	delete(bucket, "billing.sql.gz.gpg")
	res := checkSetAgainstBucket(setWithItems(t, true), bucket, verifyPass)
	if res.OK {
		t.Error("a set missing an artifact passed verification")
	}
	if len(res.Missing) != 1 || res.Missing[0] != "billing.sql.gz.gpg" {
		t.Errorf("missing = %v, want billing named", res.Missing)
	}
	if !strings.Contains(res.Error, "billing.sql.gz.gpg") {
		t.Errorf("error = %q, want it to name the artifact", res.Error)
	}
}

// An object of the wrong size is not the object this set stored.
func TestVerifyCatchesAResizedArtifact(t *testing.T) {
	bucket := intactBucket()
	bucket["orders.sql.gz.gpg"] = 7
	res := checkSetAgainstBucket(setWithItems(t, true), bucket, verifyPass)
	if res.OK {
		t.Error("a resized artifact passed verification")
	}
	if len(res.Resized) != 1 {
		t.Errorf("resized = %v", res.Resized)
	}
}

// A set can be perfectly intact in the bucket and still unreadable.
func TestVerifyFailsWhenThePassphraseNoLongerOpensIt(t *testing.T) {
	res := checkSetAgainstBucket(setWithItems(t, true), intactBucket(), "a-different-passphrase-2")
	if res.OK || res.EnvelopeOK {
		t.Error("a set sealed with another passphrase passed verification")
	}
	if !strings.Contains(res.Error, "no longer opens") {
		t.Errorf("error = %q", res.Error)
	}

	res = checkSetAgainstBucket(setWithItems(t, true), intactBucket(), "")
	if res.OK || !strings.Contains(res.Error, "no workspace backup passphrase") {
		t.Errorf("with no passphrase: OK=%v error=%q", res.OK, res.Error)
	}
}

func TestVerifyUnencryptedSet(t *testing.T) {
	res := checkSetAgainstBucket(setWithItems(t, false), intactBucket(), "")
	if !res.OK || !res.EnvelopeOK {
		t.Errorf("an intact unencrypted set failed: %q", res.Error)
	}
}

func TestVerifyEmptySet(t *testing.T) {
	res := checkSetAgainstBucket(&models.DatabaseBackupSet{Ref: "empty"}, map[string]int64{}, "")
	if res.OK || !strings.Contains(res.Error, "no artifacts") {
		t.Errorf("OK=%v error=%q", res.OK, res.Error)
	}
}

// A PostgreSQL 17 dump does not load into 16, and the failure lands mid-restore
// over a database a force run has already dropped.
func TestCheckRestoreVersion(t *testing.T) {
	cases := []struct {
		name, inst, backup string
		wantErr            bool
	}{
		{"same major", "17", "17", false},
		{"older dump into newer engine", "17", "16", false},
		{"newer dump into older engine", "16", "17", true},
		{"minor versions ignored", "17.2", "17.4", false},
		{"mysql style", "8.0.35", "8.0.36", false},
		{"mysql major back", "5.7", "8.0", true},
		{"unparseable backup version", "17", "", false},
		{"unparseable instance version", "", "17", false},
		{"both unparseable", "latest", "latest", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRestoreVersion(tc.inst, tc.backup)
			if tc.wantErr != (err != nil) {
				t.Fatalf("checkRestoreVersion(%q, %q) = %v, wantErr %v", tc.inst, tc.backup, err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, ErrRestoreVersionTooNew) {
				t.Errorf("error = %v, want ErrRestoreVersionTooNew", err)
			}
		})
	}
}
