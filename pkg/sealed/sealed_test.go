// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package sealed

import (
	"errors"
	"strings"
	"testing"
)

func mustKey(t *testing.T) (string, string) {
	t.Helper()
	id, rcpt, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return id, rcpt
}

func TestSealOpenRoundTrip(t *testing.T) {
	id, rcpt := mustKey(t)
	s, err := Seal(rcpt, 3, "ghcr-token", "ghp_secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "sealed:v1:3:") {
		t.Fatalf("unexpected format %q", s)
	}
	ver, _, err := Parse(s)
	if err != nil || ver != 3 {
		t.Fatalf("Parse = %d, %v", ver, err)
	}
	v, used, err := Open(s, "ghcr-token", id)
	if err != nil || v != "ghp_secret" || used != 0 {
		t.Fatalf("Open = %q, %d, %v", v, used, err)
	}
}

func TestOpenRefusesAnotherSecretName(t *testing.T) {
	id, rcpt := mustKey(t)
	s, _ := Seal(rcpt, 1, "stripe-key", "sk_live")
	_, _, err := Open(s, "public-banner", id)
	var mm *NameMismatchError
	if !errors.As(err, &mm) || mm.SealedFor != "stripe-key" {
		t.Fatalf("want name mismatch, got %v", err)
	}
}

func TestOpenRefusesAnotherWorkspaceKey(t *testing.T) {
	_, rcpt := mustKey(t)
	other, _ := mustKey(t)
	s, _ := Seal(rcpt, 1, "db", "pw")
	if _, _, err := Open(s, "db", other); !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
}

func TestOpenTriesEveryRetainedKey(t *testing.T) {
	old, oldRcpt := mustKey(t)
	cur, _ := mustKey(t)
	s, _ := Seal(oldRcpt, 0, "db", "pw")
	v, used, err := Open(s, "db", cur, old)
	if err != nil || v != "pw" || used != 1 {
		t.Fatalf("Open = %q, %d, %v", v, used, err)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "plain", "sealed:v1:", "sealed:v1:x:AAAA", "sealed:v1:1:!!!", "sealed:v2:1:AAAA"} {
		if _, _, err := Parse(s); !errors.Is(err, ErrFormat) {
			t.Errorf("Parse(%q) = %v, want ErrFormat", s, err)
		}
	}
}

func TestFingerprintChangesOnReseal(t *testing.T) {
	_, rcpt := mustKey(t)
	a, _ := Seal(rcpt, 1, "db", "pw")
	b, _ := Seal(rcpt, 1, "db", "pw")
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("two seals of the same value must differ")
	}
	if Fingerprint(a) != Fingerprint(" "+a+"\n") {
		t.Fatal("fingerprint must ignore surrounding whitespace")
	}
}
