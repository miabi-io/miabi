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

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeRunAsUser(t *testing.T) {
	valid := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"  1000  ", "1000"},
		{"1000:1000", "1000:1000"},
		{"0", "0"}, // shape only; the profile check rejects root, not this
		{"node", "node"},
		{"node:node", "node:node"},
		{"app_user", "app_user"},
		{"app-user.v2:app-group", "app-user.v2:app-group"},
	}
	for _, tc := range valid {
		got, err := NormalizeRunAsUser(tc.in)
		if err != nil {
			t.Errorf("NormalizeRunAsUser(%q) errored: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("NormalizeRunAsUser(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	invalid := []string{
		":",
		":1000",       // no user
		"1000:",       // no group
		"1000:1000:1", // a third field is not a thing
		"-user",       // a leading dash is not a valid account name
		".user",
		"user name",  // whitespace would confuse the engine's parser
		"user;id",    // shell metacharacters have no business here
		"user\nroot", // newline injection
		strings.Repeat("u", maxRunAsPart+1),
	}
	for _, in := range invalid {
		if _, err := NormalizeRunAsUser(in); !errors.Is(err, ErrRunAsUserInvalid) {
			t.Errorf("NormalizeRunAsUser(%q) should be invalid, got %v", in, err)
		}
	}
}

func TestRunAsUserIsNonRoot(t *testing.T) {
	nonRoot := []string{"1", "1000", "1000:1000", "1000:0", "65534:65534"}
	for _, v := range nonRoot {
		if !RunAsUserIsNonRoot(v) {
			t.Errorf("%q should count as non-root", v)
		}
	}

	// A name resolves via the image's own /etc/passwd, so it is free to be uid 0.
	rooted := []string{"", "0", "0:0", "root", "root:root", "appuser", "nobody", "1000:staff", "-1"}
	for _, v := range rooted {
		if RunAsUserIsNonRoot(v) {
			t.Errorf("%q must not count as verifiably non-root", v)
		}
	}
}
