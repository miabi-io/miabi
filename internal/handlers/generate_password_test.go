// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"
	"testing"
)

// TestGeneratePassword checks the admin-reset password generator: fixed length,
// only characters from the unambiguous alphabet, and distinct across calls.
func TestGeneratePassword(t *testing.T) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		pw, err := generatePassword()
		if err != nil {
			t.Fatalf("generatePassword: %v", err)
		}
		if len(pw) != 20 {
			t.Fatalf("length = %d, want 20 (%q)", len(pw), pw)
		}
		for _, r := range pw {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("password %q contains disallowed char %q", pw, r)
			}
		}
		if seen[pw] {
			t.Fatalf("duplicate password generated: %q", pw)
		}
		seen[pw] = true
	}
}
