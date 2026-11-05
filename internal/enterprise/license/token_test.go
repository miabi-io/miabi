// Copyright (c) 2026 Jonas Kaninda. All rights reserved.
//
// This file is part of Miabi Enterprise Edition.
//
// This file is licensed under the Miabi Enterprise License, not the GNU Affero
// General Public License. You may use it only with a valid Miabi Enterprise
// subscription and under the terms in internal/enterprise/LICENSE.md.
//
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type testKey struct {
	kid  string
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newKey(t *testing.T, kid string) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{kid, pub, priv}
}

func sign(t *testing.T, product string, k testKey, c Claims) string {
	t.Helper()
	c.Product = product
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	body := product + "-v1." + k.kid + "." + b64.EncodeToString(payload)
	return body + "." + b64.EncodeToString(ed25519.Sign(k.priv, []byte(body)))
}

func keyring(t *testing.T, product string, keys ...testKey) Keyring {
	t.Helper()
	doc := map[string]any{"product": product, "keys": map[string]string{}}
	for _, k := range keys {
		doc["keys"].(map[string]string)[k.kid] = base64.StdEncoding.EncodeToString(k.pub)
	}
	raw, _ := json.Marshal(doc)
	ring, err := ParseKeyring(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func sampleClaims(now time.Time) Claims {
	return Claims{
		LicenseID: "lic_test", Customer: "Acme GmbH", Edition: "enterprise",
		Flags:     []string{"sso_saml", "ha"},
		Limits:    map[string]int{"node_limit": 8},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour),
		GraceDays: 14, IssuedAt: now,
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	k := newKey(t, "miabi-202611")
	tok := sign(t, Product, k, sampleClaims(time.Now()))
	if KeyID(tok) != "miabi-202611" {
		t.Fatalf("KeyID = %q", KeyID(tok))
	}
	got, err := Verify(keyring(t, Product, k), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.LicenseID != "lic_test" || got.Edition != "enterprise" || got.Limits["node_limit"] != 8 {
		t.Fatalf("claims round-trip mismatch: %+v", got)
	}
}

// TestRotation: a build trusting old and new keys verifies licenses signed by either.
func TestRotation(t *testing.T) {
	old, next := newKey(t, "miabi-2026"), newKey(t, "miabi-2027")
	ring := keyring(t, Product, old, next)
	for _, k := range []testKey{old, next} {
		if _, err := Verify(ring, sign(t, Product, k, sampleClaims(time.Now()))); err != nil {
			t.Fatalf("%s: %v", k.kid, err)
		}
	}
	if _, err := Verify(keyring(t, Product, old), sign(t, Product, next, sampleClaims(time.Now()))); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("a key the build does not hold: err = %v", err)
	}
}

func TestVerifyTamperFails(t *testing.T) {
	k := newKey(t, "k1")
	tok := sign(t, Product, k, sampleClaims(time.Now()))
	b := []byte(tok)
	b[len(b)/2] ^= 0x01
	if _, err := Verify(keyring(t, Product, k), string(b)); err == nil {
		t.Fatal("expected verification to fail on a tampered token")
	}
}

func TestVerifyWrongKeyFails(t *testing.T) {
	k, other := newKey(t, "k1"), newKey(t, "k1")
	tok := sign(t, Product, k, sampleClaims(time.Now()))
	if _, err := Verify(keyring(t, Product, other), tok); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature, got %v", err)
	}
	k2 := newKey(t, "k2")
	parts := strings.Split(tok, ".")
	swapped := strings.Join([]string{parts[0], "k2", parts[2], parts[3]}, ".")
	if _, err := Verify(keyring(t, Product, k, k2), swapped); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("swapped kid: err = %v", err)
	}
}

func TestVerifyOtherProduct(t *testing.T) {
	k := newKey(t, "k1")
	posta := sign(t, "posta", k, sampleClaims(time.Now()))
	if _, err := Verify(keyring(t, Product, k), posta); !errors.Is(err, ErrMalformed) {
		t.Fatalf("posta token: err = %v", err)
	}
	if _, err := Verify(keyring(t, "posta", k), sign(t, Product, k, sampleClaims(time.Now()))); !errors.Is(err, ErrWrongProduct) {
		t.Fatalf("posta keyring: err = %v", err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	ring := keyring(t, Product, newKey(t, "k1"))
	for _, tok := range []string{"", "nope", "miabi-v1.onlytwo", "miabi-v1.a.b", "wrong-prefix.k1.a.b"} {
		if _, err := Verify(ring, tok); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%q: err = %v", tok, err)
		}
	}
}

func TestParseKeyring(t *testing.T) {
	for _, raw := range []string{"", "not base64!", `{"product":"miabi","keys":{}}`, `{"product":"miabi","keys":{"k":"c2hvcnQ="}}`} {
		if _, err := ParseKeyring(raw); !errors.Is(err, ErrBadKeyring) {
			t.Fatalf("%q: err = %v", raw, err)
		}
	}
}

func TestEvaluateStates(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	base := Claims{Edition: "enterprise", Flags: []string{"ha"}, GraceDays: 14}

	cases := []struct {
		name      string
		notBefore time.Time
		notAfter  time.Time
		want      State
	}{
		{"valid", now.Add(-24 * time.Hour), now.Add(24 * time.Hour), StateValid},
		{"grace", now.Add(-48 * time.Hour), now.Add(-1 * time.Hour), StateGrace},
		{"degraded", now.Add(-100 * 24 * time.Hour), now.Add(-30 * 24 * time.Hour), StateDegraded},
		{"not-yet-active", now.Add(24 * time.Hour), now.Add(48 * time.Hour), StateNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			c.NotBefore, c.NotAfter = tc.notBefore, tc.notAfter
			got := Evaluate(c, now)
			if got.State != tc.want {
				t.Fatalf("state = %q, want %q", got.State, tc.want)
			}
			if !got.Flags["ha"] {
				t.Fatal("expected ha flag set in snapshot")
			}
		})
	}
}
