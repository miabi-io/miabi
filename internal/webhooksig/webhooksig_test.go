// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// An unconfigured webhook must not verify anything. The HMAC under an empty key
// is computable by anyone, so without the guard the caller signs its own payload
// and the endpoint accepts it.
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
