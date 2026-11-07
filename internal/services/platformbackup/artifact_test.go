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
	"testing"
)

// With encryption pg-bkup names the plain dump first; taking the first match recorded a file never uploaded.
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

// An old helper ignores GPG_PASSPHRASE; record what it produced so restore doesn't look for a missing .gpg.
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

// An endpoint's scheme overrides USE_SSL so the Go client and helper containers never pick different transports.
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

// Retrying permanent failures buries the cause; skipping transient ones loses the recovery point. Default: retry.
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

// MinIO ignores the region, but the AWS SDK in the *-bkup helpers refuses to start without one.
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
