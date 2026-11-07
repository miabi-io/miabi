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

package registry

import (
	"strings"
	"testing"
)

func TestFingerprint(t *testing.T) {
	const pw = "ghp_secret_token"

	if got := Fingerprint("ghcr", pw); got != Fingerprint("ghcr", pw) {
		t.Error("fingerprint must be stable for the same name+password")
	}
	if Fingerprint("ghcr", pw) == Fingerprint("ghcr", pw+"!") {
		t.Error("a rotated password must change the fingerprint")
	}
	// Salted by name so the same password under two credentials does not correlate.
	if Fingerprint("ghcr", pw) == Fingerprint("dockerhub", pw) {
		t.Error("fingerprints must be salted by the credential name")
	}
	// Nothing recoverable, and an unset password is distinguishable from a set one.
	if fp := Fingerprint("ghcr", pw); strings.Contains(fp, pw) || len(fp) != 16 {
		t.Errorf("fingerprint = %q, want a 16-char digest containing no plaintext", fp)
	}
	if Fingerprint("ghcr", "") != "" {
		t.Error("an unset password must fingerprint to the empty string")
	}
}
