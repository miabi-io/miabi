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

package secret

import "testing"

func TestRefName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"${{ secrets.GHCR_TOKEN }}", "GHCR_TOKEN"},
		{"${{secrets.GHCR_TOKEN}}", "GHCR_TOKEN"}, // spacing is not significant
		{"  ${{ secrets.a-b_c }}  ", "a-b_c"},
		{"", ""},
		{"ghp_averyrealtoken", ""},
		// A credential is opaque: a token that merely contains the sequence must not become a dangling reference.
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
