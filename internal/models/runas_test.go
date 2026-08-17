// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

	// Root itself, and anything the platform cannot verify: a name is resolved from
	// the image's own /etc/passwd, so it is free to be uid 0.
	rooted := []string{"", "0", "0:0", "root", "root:root", "appuser", "nobody", "1000:staff", "-1"}
	for _, v := range rooted {
		if RunAsUserIsNonRoot(v) {
			t.Errorf("%q must not count as verifiably non-root", v)
		}
	}
}
