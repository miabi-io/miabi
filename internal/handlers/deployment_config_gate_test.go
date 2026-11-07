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

import "testing"

// The registry mirror needs private_registry to change it, not to keep it.
func TestMirrorChangeRefused(t *testing.T) {
	cases := []struct {
		name               string
		current, requested string
		editable           bool
		want               bool
	}{
		{"entitled: may set one", "", "registry.acme.example", true, false},
		{"entitled: may change one", "old.example", "new.example", true, false},
		{"entitled: may clear one", "old.example", "", true, false},

		{"community: may not set one", "", "registry.acme.example", false, true},
		{"community: may not change one", "old.example", "new.example", false, true},
		// Clearing is a change: it would stop an air-gapped install resolving images through its mirror.
		{"community: may not clear one", "old.example", "", false, true},

		// The form round-trips the stored value on every save; per-image overrides are Community.
		{"community: unchanged mirror passes", "old.example", "old.example", false, false},
		{"community: no mirror at all passes", "", "", false, false},

		// Resolver.Mirror() strips a trailing slash, so the form can send one back.
		{"community: trailing slash is not a change", "old.example", "old.example/", false, false},
		{"community: whitespace is not a change", "old.example", "  old.example  ", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mirrorChangeRefused(c.current, c.requested, c.editable); got != c.want {
				t.Errorf("mirrorChangeRefused(%q, %q, editable=%v) = %v, want %v",
					c.current, c.requested, c.editable, got, c.want)
			}
		})
	}
}
