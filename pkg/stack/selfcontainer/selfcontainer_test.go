// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfcontainer

import "testing"

func TestMatch(t *testing.T) {
	full := "3f5d2c1a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170615243"
	short := full[:12]

	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"full equals full", full, full, true},
		{"short prefix of full", short, full, true},
		{"full has short prefix", full, short, true},
		{"empty self never matches", "", full, false},
		{"empty target never matches", full, "", false},
		{"too short to match", full[:8], full, false},
		{"different ids", full, "a" + full[1:], false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.a, tc.b); got != tc.want {
				t.Fatalf("Match(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestDetectEnvOverride(t *testing.T) {
	t.Setenv("MIABI_CONTAINER_ID", "  deadbeefcafe  ")
	if got := Detect(); got != "deadbeefcafe" {
		t.Fatalf("Detect() = %q, want trimmed override", got)
	}
}
