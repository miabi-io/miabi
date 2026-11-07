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

package backupsettings

import (
	"reflect"
	"testing"
)

// Each prefix once: policies are prefix-scoped, but repeating a path is noise.
func TestDistinctPrefixes(t *testing.T) {
	cases := []struct {
		name string
		in   SaveInput
		want []string
	}{
		{
			name: "three distinct paths are each tested",
			in: SaveInput{
				DatabaseBackupPath: "backups/databases",
				VolumeBackupPath:   "backups/volumes",
				BundlePath:         "bundles",
			},
			want: []string{"backups/databases", "backups/volumes", "bundles"},
		},
		{
			name: "a shared path is tested once",
			in: SaveInput{
				DatabaseBackupPath: "backups",
				VolumeBackupPath:   "backups",
				BundlePath:         "backups",
			},
			want: []string{"backups"},
		},
		{
			name: "unset paths collapse to the bucket root",
			in:   SaveInput{},
			want: []string{""},
		},
		{
			name: "slashes and spaces do not create a second prefix",
			in: SaveInput{
				DatabaseBackupPath: "backups/",
				VolumeBackupPath:   " /backups ",
				BundlePath:         "bundles",
			},
			want: []string{"backups", "bundles"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := distinctPrefixes(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("distinctPrefixes() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A region mismatch surfaces as a credential problem rather than a region one.
func TestDefaultRegionMatchesTheHelpers(t *testing.T) {
	if got := defaultRegion(""); got != "us-east-1" {
		t.Fatalf("defaultRegion(%q) = %q, want us-east-1", "", got)
	}
	if got := defaultRegion("eu-central-1"); got != "eu-central-1" {
		t.Fatalf("defaultRegion rewrote a configured region: %q", got)
	}
}
