// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package dr

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func sample() *Identity {
	return &Identity{
		InstallID:     "mbi_0123456789abcdef",
		MiabiVersion:  "1.7.3",
		EncryptionKey: "b2f1c0de5e3a4f8b9c1d2e3f4a5b6c7d",
		JWTSecret:     "jwt-secret-value",
		Domain:        "miabi.example.com",
		CreatedAt:     time.Unix(1_760_000_000, 0).UTC(),
	}
}

const goodPass = "correct-horse-9!"

func TestSealOpenRoundTrip(t *testing.T) {
	in := sample()
	sealed, err := Seal(in, goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	out, err := Open(sealed, goodPass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if out.EncryptionKey != in.EncryptionKey || out.InstallID != in.InstallID || out.JWTSecret != in.JWTSecret {
		t.Fatalf("round trip lost fields: %+v", out)
	}
	if out.Schema != IdentitySchema {
		t.Fatalf("schema = %d, want %d", out.Schema, IdentitySchema)
	}
}

// The envelope is the one artifact whose plaintext is the whole platform. If the
// encryption key ever appears in the sealed bytes, everything else is theatre.
func TestSealedEnvelopeLeaksNothing(t *testing.T) {
	in := sample()
	sealed, err := Seal(in, goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, secret := range []string{in.EncryptionKey, in.JWTSecret, in.InstallID, in.Domain} {
		if bytes.Contains(sealed, []byte(secret)) {
			t.Fatalf("sealed envelope contains plaintext %q", secret)
		}
	}
}

func TestOpenWrongPassphrase(t *testing.T) {
	sealed, err := Seal(sample(), goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Open(sealed, "correct-horse-9?"); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("err = %v, want ErrBadPassphrase", err)
	}
}

// Every byte of the header is authenticated, so an edited salt or version must
// fail to open rather than decrypt to something.
func TestOpenTamperedEnvelope(t *testing.T) {
	base, err := Seal(sample(), goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, tc := range []struct {
		name string
		at   int
		want error
	}{
		{"magic", 1, ErrNotEnvelope},
		{"salt", len(magic) + 2, ErrBadPassphrase},
		{"nonce", len(magic) + 1 + saltLen + 1, ErrBadPassphrase},
		{"ciphertext", headerLen + 1, ErrBadPassphrase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sealed := append([]byte(nil), base...)
			sealed[tc.at] ^= 0xFF
			if _, err := Open(sealed, goodPass); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestOpenNotAnEnvelope(t *testing.T) {
	if _, err := Open([]byte("hello"), goodPass); !errors.Is(err, ErrNotEnvelope) {
		t.Fatalf("err = %v, want ErrNotEnvelope", err)
	}
}

func TestSealIsNondeterministic(t *testing.T) {
	a, err := Seal(sample(), goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	b, err := Seal(sample(), goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same identity produced identical bytes (salt/nonce reuse)")
	}
}

func TestValidatePassphrase(t *testing.T) {
	for _, tc := range []struct {
		pass string
		ok   bool
	}{
		{"correct-horse-9!", true},
		{"Password12345", true},
		{"short1!", false},
		{"alllettersonly", false},
		{"1234567890123", false},
		{"", false},
	} {
		err := ValidatePassphrase(tc.pass)
		if tc.ok && err != nil {
			t.Errorf("ValidatePassphrase(%q) = %v, want nil", tc.pass, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("ValidatePassphrase(%q) = nil, want error", tc.pass)
		}
	}
}

func TestSealRejectsWeakPassphrase(t *testing.T) {
	if _, err := Seal(sample(), "weak"); !errors.Is(err, ErrWeakPassphrase) {
		t.Fatalf("err = %v, want ErrWeakPassphrase", err)
	}
}

func TestOpenRejectsIdentityWithoutKey(t *testing.T) {
	id := sample()
	id.EncryptionKey = ""
	// Seal validates the passphrase, not the payload, so this reaches Open.
	sealed, err := Seal(id, goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	_, err = Open(sealed, goodPass)
	if err == nil || !strings.Contains(err.Error(), "no encryption key") {
		t.Fatalf("err = %v, want a missing-encryption-key error", err)
	}
}

func TestRefRoundTrip(t *testing.T) {
	ref := NewRef("mbi_abc", time.Unix(1_760_000_000, 0).UTC())
	if !IsRef(ref) {
		t.Fatalf("IsRef(%q) = false", ref)
	}
	key := IdentityObject("platform/db", ref)
	if got := RefFromIdentityObject(key); got != ref {
		t.Fatalf("RefFromIdentityObject(%q) = %q, want %q", key, got, ref)
	}
	if got := RefFromIdentityObject("platform/db/miabi_20260101.sql.gz"); got != "" {
		t.Fatalf("RefFromIdentityObject(dump) = %q, want empty", got)
	}
}
