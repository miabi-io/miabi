// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package dbenvelope

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const pass = "correct-horse-battery-9"

func TestSealOpenRoundTrip(t *testing.T) {
	key, err := NewDataKey()
	if err != nil {
		t.Fatalf("NewDataKey: %v", err)
	}
	sealed, err := Seal(key, pass)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if strings.Contains(sealed, key) {
		t.Fatal("the sealed envelope contains the data key in the clear")
	}
	got, err := Open(sealed, pass)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != key {
		t.Errorf("round trip = %q, want %q", got, key)
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	a, err := Seal("the-key", pass)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Seal("the-key", pass)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("sealing the same key twice produced identical bytes — the salt or nonce is not fresh")
	}
}

func TestOpenRejectsTheWrongPassphrase(t *testing.T) {
	sealed, err := Seal("the-key", pass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(sealed, "not-the-passphrase-1"); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("error = %v, want ErrBadPassphrase", err)
	}
}

// The header is authenticated as additional data, so editing it must fail to open
// rather than decrypt to nonsense.
func TestOpenRejectsATamperedEnvelope(t *testing.T) {
	sealed, err := Seal("the-key", pass)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]int{
		"salt":       len(magic) + 1,
		"nonce":      len(magic) + 1 + saltLen,
		"ciphertext": headerLen,
	}
	for name, at := range cases {
		t.Run(name, func(t *testing.T) {
			bad := make([]byte, len(raw))
			copy(bad, raw)
			bad[at] ^= 0xff
			if _, err := Open(base64.StdEncoding.EncodeToString(bad), pass); err == nil {
				t.Errorf("a tampered %s opened successfully", name)
			}
		})
	}
}

func TestOpenRejectsForeignBytes(t *testing.T) {
	cases := map[string]string{
		"not base64":  "!!!not base64!!!",
		"too short":   base64.StdEncoding.EncodeToString([]byte("MBDK1")),
		"wrong magic": base64.StdEncoding.EncodeToString(append([]byte("XXXXX"), make([]byte, headerLen)...)),
		"empty":       "",
		"dr envelope": base64.StdEncoding.EncodeToString(append([]byte("MBID1"), make([]byte, headerLen)...)),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Open(in, pass); !errors.Is(err, ErrNotEnvelope) {
				t.Errorf("error = %v, want ErrNotEnvelope", err)
			}
		})
	}
}

// A future format change must be a clean refusal, not a garbled decrypt.
func TestOpenRefusesAnUnknownFormatVersion(t *testing.T) {
	sealed, err := Seal("the-key", pass)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(sealed)
	raw[len(magic)] = 99
	_, err = Open(base64.StdEncoding.EncodeToString(raw), pass)
	if !errors.Is(err, ErrNotEnvelope) {
		t.Errorf("error = %v, want ErrNotEnvelope", err)
	}
}

// The point of the whole indirection: rotating the passphrase rewrites the
// envelope and leaves the data key — and therefore every artifact — untouched.
func TestRewrapKeepsTheDataKey(t *testing.T) {
	key, err := NewDataKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(key, pass)
	if err != nil {
		t.Fatal(err)
	}
	const newPass = "a-different-passphrase-7"
	rewrapped, err := Rewrap(sealed, pass, newPass)
	if err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	got, err := Open(rewrapped, newPass)
	if err != nil {
		t.Fatalf("Open after rewrap: %v", err)
	}
	if got != key {
		t.Errorf("data key changed across rotation: %q, want %q", got, key)
	}
	// The old passphrase must no longer open the rewrapped envelope.
	if _, err := Open(rewrapped, pass); !errors.Is(err, ErrBadPassphrase) {
		t.Error("the old passphrase still opens the rewrapped envelope")
	}
}

func TestRewrapRefusesTheWrongOldPassphrase(t *testing.T) {
	sealed, err := Seal("the-key", pass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Rewrap(sealed, "wrong-passphrase-2", "new-passphrase-3"); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("error = %v, want ErrBadPassphrase", err)
	}
}

func TestSealRefusesEmptyInput(t *testing.T) {
	if _, err := Seal("", pass); err == nil {
		t.Error("sealed an empty data key")
	}
	if _, err := Seal("the-key", ""); err == nil {
		t.Error("sealed without a passphrase")
	}
}

func TestNewDataKeyIsFreshAndLongEnough(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		k, err := NewDataKey()
		if err != nil {
			t.Fatal(err)
		}
		if seen[k] {
			t.Fatal("NewDataKey repeated a value")
		}
		seen[k] = true
		raw, err := base64.RawURLEncoding.DecodeString(k)
		if err != nil {
			t.Fatalf("data key is not base64: %v", err)
		}
		if len(raw) != DataKeyLen {
			t.Fatalf("data key is %d bytes, want %d", len(raw), DataKeyLen)
		}
	}
}

func TestIsEnvelope(t *testing.T) {
	sealed, err := Seal("the-key", pass)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEnvelope(sealed) {
		t.Error("a sealed envelope is not recognised")
	}
	for _, s := range []string{"", "plain text", base64.StdEncoding.EncodeToString([]byte("nope"))} {
		if IsEnvelope(s) {
			t.Errorf("%q was taken for an envelope", s)
		}
	}
}
