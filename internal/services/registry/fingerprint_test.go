// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// Salted by name: the same password under two credentials must not collide,
	// so a plan can't correlate them.
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
