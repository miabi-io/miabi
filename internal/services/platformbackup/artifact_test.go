// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"testing"
)

// The failure this pins: with encryption on, pg-bkup names the plain dump while
// working and the encrypted one it uploads. Taking the FIRST match recorded
// "…sql.gz" while the bucket held "…sql.gz.gpg", producing a recovery point that
// looked complete and referenced objects that were never written — visible only
// on verification, or on the day someone needed to restore it.
func TestArtifactNameTakesTheUploadedFile(t *testing.T) {
	out := `Starting backup task...
Dumping database miabi to /backup/miabi_20260731_122549.sql.gz
Backup file created: miabi_20260731_122549.sql.gz
Encrypting backup file miabi_20260731_122549.sql.gz.gpg
Uploading backup archive to S3 ... miabi_20260731_122549.sql.gz.gpg
Backup completed`

	got, encrypted, err := artifactName(out, dbArtifactRe)
	if err != nil {
		t.Fatalf("artifactName: %v", err)
	}
	if got != "miabi_20260731_122549.sql.gz.gpg" {
		t.Fatalf("artifactName = %q, want the encrypted artifact that was uploaded", got)
	}
	if !encrypted {
		t.Error("a .gpg artifact was not reported as encrypted")
	}
}

func TestArtifactNameUnencrypted(t *testing.T) {
	out := "Backup file created: miabi_20260731.sql.gz\nUploading miabi_20260731.sql.gz\nDone"
	got, encrypted, err := artifactName(out, dbArtifactRe)
	if err != nil {
		t.Fatalf("artifactName: %v", err)
	}
	if got != "miabi_20260731.sql.gz" {
		t.Fatalf("artifactName = %q", got)
	}
	if encrypted {
		t.Error("a plain artifact was reported as encrypted")
	}
}

// A helper too old to support encryption ignores GPG_PASSPHRASE and writes a
// plain archive. Recording it as encrypted would point a restore at a ".gpg"
// object that does not exist; refusing the run outright would discard a backup
// that is perfectly restorable. Record the truth — the caller warns.
func TestArtifactNameRecordsWhatTheHelperProduced(t *testing.T) {
	out := "Creating archive mb-data_20260731.tar.gz\nUploading archive mb-data_20260731.tar.gz"
	got, encrypted, err := artifactName(out, volArtifactRe)
	if err != nil {
		t.Fatalf("artifactName: %v", err)
	}
	if got != "mb-data_20260731.tar.gz" {
		t.Fatalf("artifactName = %q", got)
	}
	if encrypted {
		t.Fatal("a plain archive from an old helper was recorded as encrypted")
	}
}

func TestArtifactNameNoMatch(t *testing.T) {
	if _, _, err := artifactName("nothing useful here", dbArtifactRe); err == nil {
		t.Fatal("output naming no artifact was accepted")
	}
}

func TestArtifactNameVolumes(t *testing.T) {
	out := "Creating archive mb-data_20260731.tar.gz\nEncrypting mb-data_20260731.tar.gz.gpg\nUploaded"
	got, encrypted, err := artifactName(out, volArtifactRe)
	if err != nil || got != "mb-data_20260731.tar.gz.gpg" || !encrypted {
		t.Fatalf("artifactName = %q, %v, %v", got, encrypted, err)
	}
}

// An endpoint that states its scheme is a more specific statement than the flag.
// "http://minio:9000" with USE_SSL=true is a contradiction that is easy to write
// and hard to see, and letting the two disagree lets the Go client and the
// helper containers pick different transports.
func TestEffectiveUseSSLFollowsTheEndpointScheme(t *testing.T) {
	cases := []struct {
		endpoint   string
		configured bool
		want       bool
	}{
		{"http://10.25.10.16:9000", true, false},
		{"https://s3.example.com", false, true},
		{"10.25.10.16:9000", true, true},   // no scheme: the flag decides
		{"10.25.10.16:9000", false, false}, // no scheme: the flag decides
		{"", true, true},                   // AWS default endpoint
	}
	for _, tc := range cases {
		if got := effectiveUseSSL(tc.endpoint, tc.configured); got != tc.want {
			t.Errorf("effectiveUseSSL(%q, %v) = %v, want %v", tc.endpoint, tc.configured, got, tc.want)
		}
	}
}

// Retrying a permanent failure wastes a minute and buries the cause under
// identical repeated output; refusing to retry a transient one costs the
// recovery point. The classification defaults to retrying.
func TestWorthRetrying(t *testing.T) {
	transient := []string{
		"connection reset by peer",
		"dial tcp 10.25.10.16:9000: connect: connection refused",
		"500 Internal Server Error",
		"",
	}
	for _, out := range transient {
		if !worthRetrying(out, nil) {
			t.Errorf("%q should be retried", out)
		}
	}

	permanent := []string{
		"Access Denied",
		"The specified bucket does not exist",
		"SignatureDoesNotMatch",
		"password authentication failed for user \"miabi\"",
	}
	for _, out := range permanent {
		if worthRetrying(out, nil) {
			t.Errorf("%q should not be retried", out)
		}
	}
}

// MinIO ignores the region, so leaving it blank looks harmless — but the AWS SDK
// inside the *-bkup helpers refuses to start without one, and the failure lands
// on the artifact rather than on the setting that caused it. Found by running a
// real backup against a real MinIO.
func TestS3RegionDefaults(t *testing.T) {
	if got := s3Region(""); got != defaultS3Region {
		t.Errorf("s3Region(\"\") = %q, want %q", got, defaultS3Region)
	}
	if got := s3Region("   "); got != defaultS3Region {
		t.Errorf("s3Region(blank) = %q, want %q", got, defaultS3Region)
	}
	if got := s3Region("eu-central-1"); got != "eu-central-1" {
		t.Errorf("s3Region overrode a configured region: %q", got)
	}
}
