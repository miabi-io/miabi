// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "testing"

// Accounts written before auth_source existed carry an empty string, and they are
// local — reading them as externally managed would freeze every legacy profile.
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
