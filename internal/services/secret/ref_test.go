// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package secret

import "testing"

func TestRefName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"${{ secrets.GHCR_TOKEN }}", "GHCR_TOKEN"},
		{"${{secrets.GHCR_TOKEN}}", "GHCR_TOKEN"}, // spacing is not significant
		{"  ${{ secrets.a-b_c }}  ", "a-b_c"},
		{"", ""},
		{"ghp_averyrealtoken", ""},
		// A credential is opaque, so only a value that is *entirely* a reference
		// counts. Anything else is the literal secret — a token that happens to
		// contain the sequence must not be turned into a dangling reference.
		{"prefix-${{ secrets.TOKEN }}", ""},
		{"${{ secrets.TOKEN }} suffix", ""},
		{"${{ secrets.A }}${{ secrets.B }}", ""},
		{"${{ env.TOKEN }}", ""},
		{"{{ .secrets.TOKEN }}", ""}, // the manifest render form, not the runtime one
	} {
		if got := RefName(tc.in); got != tc.want {
			t.Errorf("RefName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRefRoundTrips(t *testing.T) {
	if got := RefName(Ref("API_KEY")); got != "API_KEY" {
		t.Errorf("Ref/RefName round trip = %q, want API_KEY", got)
	}
}
