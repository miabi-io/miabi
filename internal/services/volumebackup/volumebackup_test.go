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

package volumebackup

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/services/backup"
)

// With encryption the tool names the plain archive first; matching it records an object never written.
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

// An empty filename would look restorable and never be.
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

// A retry would otherwise fail against the container its predecessor left behind.
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
