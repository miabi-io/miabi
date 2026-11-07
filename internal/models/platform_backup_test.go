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

package models

import "testing"

func TestPlatformBackupSettingsNormalize(t *testing.T) {
	cases := []struct {
		name                      string
		in                        PlatformBackupSettings
		wantRoot, wantDB, wantVol string
		wantFormat                string
	}{
		{
			name:     "unset paths get the documented defaults",
			in:       PlatformBackupSettings{},
			wantRoot: "", wantDB: "databases", wantVol: "volumes",
		},
		{
			name:     "root scopes the defaults",
			in:       PlatformBackupSettings{RootPath: "miabi"},
			wantRoot: "miabi", wantDB: "miabi/databases", wantVol: "miabi/volumes",
		},
		{
			name:     "explicit paths are kept, under the root",
			in:       PlatformBackupSettings{RootPath: "miabi", DatabaseBackupPath: "dumps", VolumeBackupPath: "vols"},
			wantRoot: "miabi", wantDB: "miabi/dumps", wantVol: "miabi/vols",
		},
		{
			name:     "slashes are trimmed",
			in:       PlatformBackupSettings{RootPath: "/miabi/", DatabaseBackupPath: "/dumps/"},
			wantRoot: "miabi", wantDB: "miabi/dumps", wantVol: "miabi/volumes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.in
			st.Normalize()
			if st.RootPath != tc.wantRoot {
				t.Errorf("RootPath = %q, want %q", st.RootPath, tc.wantRoot)
			}
			if st.DatabaseBackupPath != tc.wantDB {
				t.Errorf("DatabaseBackupPath = %q, want %q", st.DatabaseBackupPath, tc.wantDB)
			}
			if st.VolumeBackupPath != tc.wantVol {
				t.Errorf("VolumeBackupPath = %q, want %q", st.VolumeBackupPath, tc.wantVol)
			}
		})
	}
}

// Without idempotency the prefix would grow each boot and orphan earlier artifacts.
func TestPlatformBackupSettingsNormalizeIsIdempotent(t *testing.T) {
	st := PlatformBackupSettings{RootPath: "miabi", DatabaseBackupPath: "dumps"}
	st.Normalize()
	first := st
	for i := 0; i < 5; i++ {
		st.Normalize()
	}
	if st.DatabaseBackupPath != first.DatabaseBackupPath || st.VolumeBackupPath != first.VolumeBackupPath || st.RootPath != first.RootPath {
		t.Fatalf("normalize drifted after repeated calls: %+v vs %+v", st, first)
	}
}
