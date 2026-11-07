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

// Legacy accounts with an empty auth_source are local; reading them as external would freeze their profiles.
func TestUserIsExternal(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   bool
	}{
		{"", false},
		{AuthSourceLocal, false},
		{"LOCAL", false},
		{" local ", false},
		{AuthSourceOAuth, true},
		{AuthSourceLDAP, true},
		{AuthSourceSAML, true},
		{AuthSourceSCIM, true},
	} {
		u := User{AuthSource: tc.source}
		if got := u.IsExternal(); got != tc.want {
			t.Errorf("IsExternal(%q) = %v, want %v", tc.source, got, tc.want)
		}
	}
}
