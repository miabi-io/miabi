// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import "testing"

// The registry mirror needs private_registry to CHANGE, not to keep. Everything
// here is about not punishing a Community admin for the mirror's mere existence.
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
		// Clearing is still a change: it would silently stop an air-gapped install
		// resolving images through the registry it can actually reach.
		{"community: may not clear one", "old.example", "", false, true},

		// The form round-trips the stored value on every save. Refusing here would
		// stop a Community admin editing per-image overrides, which are theirs.
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
