// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformrestore

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// The database dump and volume archives live under separately configured
// prefixes. A restore that assumed one prefix for both would look in the wrong
// place and report a perfectly good recovery point as incomplete.
func TestArtifactKeyUsesTheRightPrefix(t *testing.T) {
	man := &dr.Manifest{Prefix: "platform/db", VolumePrefix: "platform/volumes"}
	cases := []struct {
		artifact dr.Artifact
		want     string
	}{
		{dr.Artifact{Subject: "database", File: "miabi_2026.sql.gz"}, "platform/db/miabi_2026.sql.gz"},
		{dr.Artifact{Subject: "volume", Volume: "v", File: "v_2026.tar.gz"}, "platform/volumes/v_2026.tar.gz"},
	}
	for _, tc := range cases {
		if got := artifactKey(man, tc.artifact); got != tc.want {
			t.Errorf("artifactKey(%s) = %q, want %q", tc.artifact.Subject, got, tc.want)
		}
	}

	bare := &dr.Manifest{}
	if got := artifactKey(bare, dr.Artifact{Subject: "database", File: "d.sql.gz"}); got != "d.sql.gz" {
		t.Errorf("artifactKey with no prefix = %q, want a bare key", got)
	}
}

// Migrations only run forward. A dump from a newer Miabi against older code
// fails in ways that look like data corruption, well after the restore has
// reported success — so the restore refuses instead.
func TestAssertVersionForward(t *testing.T) {
	orig := config.Version
	t.Cleanup(func() { config.Version = orig })

	cases := []struct {
		name           string
		binary, backup string
		wantErr        bool
	}{
		{"same version", "1.7.3", "1.7.3", false},
		{"backup older patch", "1.7.3", "1.7.1", false},
		{"backup older minor", "1.8.0", "1.7.9", false},
		{"backup older major", "2.0.0", "1.9.9", false},
		{"backup newer patch", "1.7.1", "1.7.3", true},
		{"backup newer minor", "1.7.9", "1.8.0", true},
		{"backup newer major", "1.9.9", "2.0.0", true},
		{"v prefixes", "v1.7.3", "v1.7.3", false},
		{"pre-release suffix on backup", "1.7.3", "1.7.3-rc1", false},
		// A dev build must not block someone's recovery over a version string.
		{"dev binary", "dev", "1.7.3", false},
		{"unversioned backup", "1.7.3", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config.Version = tc.binary
			err := assertVersionForward(tc.backup)
			if tc.wantErr && err == nil {
				t.Fatalf("binary %s, backup %s: expected a refusal", tc.binary, tc.backup)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("binary %s, backup %s: unexpected refusal: %v", tc.binary, tc.backup, err)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	if v, ok := parseVersion("v1.2.3"); !ok || v != [3]int{1, 2, 3} {
		t.Fatalf("parseVersion(v1.2.3) = %v, %v", v, ok)
	}
	for _, bad := range []string{"dev", "", "1.2", "x.y.z"} {
		if _, ok := parseVersion(bad); ok {
			t.Errorf("parseVersion(%q) reported success", bad)
		}
	}
}

// The check the whole feature turns on: an identity envelope that opens but
// carries the wrong master key must be caught before anything is restored. Left
// undetected, it produces a platform that boots, lists everything, and cannot
// decrypt a single secret.
func TestKEKFingerprintDistinguishesKeys(t *testing.T) {
	const label = models.KEKFingerprintLabel
	right := crypto.DeriveTokenFrom("the-key-the-backup-was-taken-under", label)
	wrong := crypto.DeriveTokenFrom("a-different-key-entirely", label)

	if right == "" || wrong == "" {
		t.Fatal("fingerprints must not be empty for a real key")
	}
	if right == wrong {
		t.Fatal("two different master keys produced the same fingerprint")
	}
	if again := crypto.DeriveTokenFrom("the-key-the-backup-was-taken-under", label); again != right {
		t.Fatal("the same key produced two different fingerprints")
	}
	// The fingerprint must not be the key.
	if strings.Contains(right, "the-key-the-backup-was-taken-under") {
		t.Fatal("the fingerprint leaks the key")
	}
	// An absent key must not fingerprint to something that could match.
	if crypto.DeriveTokenFrom("", label) != "" {
		t.Fatal("an empty key produced a fingerprint")
	}
}

func TestSQLEscape(t *testing.T) {
	if got := sqlEscape("it's fine"); got != "it''s fine" {
		t.Fatalf("sqlEscape = %q", got)
	}
}
