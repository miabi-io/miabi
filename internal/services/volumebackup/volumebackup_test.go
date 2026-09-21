// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package volumebackup

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/services/backup"
)

// The archive name is read back off the helper's output, so the regex decides what the row points
// at. With encryption on, the tool narrates the plain archive before the encrypted one it uploads —
// matching the first or the shorter name records an object that was never written.
func TestArchiveNameFromHelperOutput(t *testing.T) {
	cases := []struct {
		name          string
		out           string
		want          string
		wantEncrypted bool
	}{
		{
			name: "plain archive",
			out:  "creating data_20260921_030000.tar.gz\nuploaded data_20260921_030000.tar.gz\n",
			want: "data_20260921_030000.tar.gz",
		},
		{
			name:          "encrypted archive wins over the plain name that precedes it",
			out:           "creating data_20260921_030000.tar.gz\nencrypting\nuploaded data_20260921_030000.tar.gz.gpg\n",
			want:          "data_20260921_030000.tar.gz.gpg",
			wantEncrypted: true,
		},
		{
			name: "dots and dashes in the volume name",
			out:  "uploaded my-app.data_20260921_030000.tar.gz\n",
			want: "my-app.data_20260921_030000.tar.gz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, encrypted, err := backup.ArtifactName(tc.out, archiveRe)
			if err != nil {
				t.Fatalf("ArtifactName: %v", err)
			}
			if got != tc.want {
				t.Errorf("name = %q, want %q", got, tc.want)
			}
			if encrypted != tc.wantEncrypted {
				t.Errorf("encrypted = %v, want %v", encrypted, tc.wantEncrypted)
			}
		})
	}
}

// A helper that reports success while naming no artifact must fail the run rather than complete with
// an empty filename, which would look restorable and never be.
func TestArchiveNameRefusesOutputWithNoArtifact(t *testing.T) {
	if _, _, err := backup.ArtifactName("nothing to do\n", archiveRe); err == nil {
		t.Fatal("expected an error when the output names no archive")
	}
}

func TestArchiveKey(t *testing.T) {
	cases := []struct{ prefix, filename, want string }{
		{"volumes", "data_20260921.tar.gz", "volumes/data_20260921.tar.gz"},
		{"/volumes/", "data.tar.gz", "volumes/data.tar.gz"},
		{" volumes/nested ", "data.tar.gz", "volumes/nested/data.tar.gz"},
		{"", "data.tar.gz", "data.tar.gz"},
	}
	for _, tc := range cases {
		if got := archiveKey(tc.prefix, tc.filename); got != tc.want {
			t.Errorf("archiveKey(%q, %q) = %q, want %q", tc.prefix, tc.filename, got, tc.want)
		}
	}
}

// Two runs of the same backup must not collide on a container name: a retry otherwise fails against
// the container its predecessor left behind.
func TestOneShotNameIsUniquePerRun(t *testing.T) {
	a, b := oneShotName("mb-volbkup", 7), oneShotName("mb-volbkup", 7)
	if a == b {
		t.Errorf("two names for the same backup are identical: %q", a)
	}
	for _, n := range []string{a, b} {
		if !strings.HasPrefix(n, "mb-volbkup-7-") {
			t.Errorf("name %q does not identify the backup it belongs to", n)
		}
	}
}
