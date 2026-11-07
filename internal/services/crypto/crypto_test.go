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

package crypto

import (
	"crypto/sha256"
	"strings"
	"testing"
)

type fakeKeyring struct{ ver int }

func (f fakeKeyring) dek(ws uint, ver int) []byte {
	h := sha256.Sum256([]byte("dek-" + string(rune(ws)) + "-" + string(rune(ver))))
	return h[:]
}
func (f fakeKeyring) ActiveDEK(ws uint) (int, []byte, error) { return f.ver, f.dek(ws, f.ver), nil }
func (f fakeKeyring) DEK(ws uint, ver int) ([]byte, error)   { return f.dek(ws, ver), nil }

func TestEncryptWSRoundTripAndIsolation(t *testing.T) {
	Init("a-strong-secret")
	SetKeyring(fakeKeyring{ver: 1})
	t.Cleanup(func() { Init(""); SetKeyring(nil) })

	encA, err := EncryptWS(1, "secret")
	if err != nil {
		t.Fatalf("encrypt ws1: %v", err)
	}
	if !strings.HasPrefix(encA, "e2:w:1:1:") {
		t.Fatalf("expected per-workspace header, got %q", encA)
	}
	encB, _ := EncryptWS(2, "secret")
	if encA == encB {
		t.Fatal("same plaintext in different workspaces must differ (per-workspace keys)")
	}
	// Each decrypts under its own workspace key.
	if got, _ := Decrypt(encA); got != "secret" {
		t.Fatalf("ws1 round trip: got %q", got)
	}
	if got, _ := Decrypt(encB); got != "secret" {
		t.Fatalf("ws2 round trip: got %q", got)
	}
}

func TestEncryptWSFallsBackWithoutKeyring(t *testing.T) {
	Init("a-strong-secret")
	SetKeyring(nil)
	t.Cleanup(func() { Init("") })
	enc, err := EncryptWS(1, "x")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !IsEncrypted(enc) || strings.HasPrefix(enc, "e2:") {
		t.Fatalf("no keyring should fall back to master key, got %q", enc)
	}
	if got, _ := Decrypt(enc); got != "x" {
		t.Fatalf("round trip: got %q", got)
	}
}

func TestDecryptLegacyAfterKeyringWired(t *testing.T) {
	// A value encrypted with the master key still decrypts once the keyring is on.
	Init("a-strong-secret")
	legacy, _ := Encrypt("old-value")
	SetKeyring(fakeKeyring{ver: 1})
	t.Cleanup(func() { Init(""); SetKeyring(nil) })
	if got, _ := Decrypt(legacy); got != "old-value" {
		t.Fatalf("legacy decrypt: got %q", got)
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	Init("a-strong-secret")
	t.Cleanup(func() { Init("") })

	const plain = "super-secret-db-password"
	enc, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !IsEncrypted(enc) {
		t.Fatalf("expected encrypted prefix, got %q", enc)
	}
	if enc == plain {
		t.Fatal("ciphertext must differ from plaintext")
	}
	got, err := Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != plain {
		t.Fatalf("round trip mismatch: got %q want %q", got, plain)
	}
}

func TestEncryptWithoutKeyIsBase64(t *testing.T) {
	Init("")
	enc, err := Encrypt("hello")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if IsEncrypted(enc) {
		t.Fatal("value must not be marked encrypted without a key")
	}
	got, err := Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "hello" {
		t.Fatalf("got %q want hello", got)
	}
}

func TestDeriveToken(t *testing.T) {
	Init("a-master-secret")
	defer Init("")

	a := DeriveToken("registry:platform-token")
	if a == "" || a == "mrt_" {
		t.Fatalf("empty derived token: %q", a)
	}
	if b := DeriveToken("registry:platform-token"); b != a {
		t.Errorf("same label not deterministic: %q != %q", a, b)
	}
	if c := DeriveToken("other-label"); c == a {
		t.Error("different labels produced the same token")
	}
	// A different master key yields a different token for the same label.
	Init("a-different-secret")
	if d := DeriveToken("registry:platform-token"); d == a {
		t.Error("token did not change with the master key")
	}
}
