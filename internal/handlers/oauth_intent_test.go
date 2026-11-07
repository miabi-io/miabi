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

package handlers

import (
	"strings"
	"testing"
)

// Intents stored before an upgrade must keep parsing, or the user's sign-in is dropped on callback.
func TestParseIntent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		intent   string
		kind     string
		redirect string
		state    string
		scopes   string
		ok       bool
	}{
		{"bare login_token", intentLoginToken, intentLoginToken, "", "", "", true},
		{
			"login_token with scopes",
			intentLoginToken + intentSep + "read",
			intentLoginToken, "", "", "read", true,
		},
		{
			"cli_login stored before scopes were carried",
			strings.Join([]string{intentCliLogin, "http://127.0.0.1:5000/callback", "st4te"}, intentSep),
			intentCliLogin, "http://127.0.0.1:5000/callback", "st4te", "", true,
		},
		{
			"cli_login with scopes",
			strings.Join([]string{intentCliLogin, "http://127.0.0.1:5000/callback", "st4te", "read,write"}, intentSep),
			intentCliLogin, "http://127.0.0.1:5000/callback", "st4te", "read,write", true,
		},
		{
			"cli_login with an empty scope field",
			strings.Join([]string{intentCliLogin, "http://127.0.0.1:5000/callback", "st4te", ""}, intentSep),
			intentCliLogin, "http://127.0.0.1:5000/callback", "st4te", "", true,
		},
		{"malformed", strings.Join([]string{"a", "b", "c", "d", "e"}, intentSep), "", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, redirect, state, scopes, ok := parseIntent(tc.intent)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if kind != tc.kind || redirect != tc.redirect || state != tc.state {
				t.Errorf("= (%q, %q, %q), want (%q, %q, %q)", kind, redirect, state, tc.kind, tc.redirect, tc.state)
			}
			if got := strings.Join(scopes, ","); got != tc.scopes {
				t.Errorf("scopes = %q, want %q", got, tc.scopes)
			}
		})
	}
}

func TestSplitScopes(t *testing.T) {
	for in, want := range map[string]string{
		"":               "",
		"   ":            "",
		"read":           "read",
		"read,write":     "read,write",
		" read , write ": "read,write",
		"read,,write":    "read,write",
	} {
		if got := strings.Join(splitScopes(in), ","); got != want {
			t.Errorf("splitScopes(%q) = %q, want %q", in, got, want)
		}
	}
}
