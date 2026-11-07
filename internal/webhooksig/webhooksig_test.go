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

package webhooksig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerify(t *testing.T) {
	const secret = "topsecret"
	body := []byte(`{"ref":"refs/heads/main"}`)

	cases := []struct {
		name      string
		secret    string
		signature string
		want      bool
	}{
		{"github hmac", secret, sign(secret, body), true},
		{"github hmac padded", secret, "  " + sign(secret, body) + "\n", true},
		{"gitlab token", secret, secret, true},
		{"forged hmac", secret, "sha256=deadbeef", false},
		{"hmac of another secret", secret, sign("guess", body), false},
		{"wrong token", secret, "guess", false},
		{"empty signature", secret, "", false},
		{"token with a shared prefix", secret, "tops", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Verify(tc.secret, tc.signature, body); got != tc.want {
				t.Errorf("Verify() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The HMAC under an empty key is computable by anyone, so an unconfigured webhook must reject everything.
func TestVerifyRejectsAnEmptySecret(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	for _, sig := range []string{sign("", body), "", "anything"} {
		if Verify("", sig, body) {
			t.Errorf("empty secret accepted signature %q", sig)
		}
	}
}

func TestVerifyIsBodyBound(t *testing.T) {
	const secret = "topsecret"
	good := sign(secret, []byte(`{"ref":"refs/heads/main"}`))
	if Verify(secret, good, []byte(`{"ref":"refs/heads/attacker"}`)) {
		t.Error("signature of one body accepted for another")
	}
}
