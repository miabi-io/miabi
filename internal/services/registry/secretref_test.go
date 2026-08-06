// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registry

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/secret"
)

// fakeVault stands in for the secret service: name -> current value.
type fakeVault map[string]string

func (v fakeVault) CredentialSecret(_ uint, enc, ref string) (string, error) {
	if ref != "" {
		val, ok := v[ref]
		if !ok {
			return "", errors.New("secret not found: " + ref)
		}
		return val, nil
	}
	if enc == "" {
		return "", nil
	}
	return crypto.Decrypt(enc)
}

func (v fakeVault) ExistsByName(_ uint, name string) bool {
	_, ok := v[name]
	return ok
}

func TestStoreSecretRoutesReferenceToTheVault(t *testing.T) {
	crypto.Init("test-master-key-for-registry-ref")
	s := &Service{secrets: fakeVault{"GHCR_TOKEN": "ghp_live"}}

	// A reference is stored by name — nothing is copied here.
	enc, ref, err := s.storeSecret(1, secret.Ref("GHCR_TOKEN"))
	if err != nil {
		t.Fatalf("storeSecret(ref): %v", err)
	}
	if ref != "GHCR_TOKEN" || enc != "" {
		t.Errorf("reference stored as enc=%q ref=%q, want the reference alone", enc, ref)
	}

	// A literal is encrypted here and holds no reference.
	enc, ref, err = s.storeSecret(1, "ghp_pasted_in")
	if err != nil {
		t.Fatalf("storeSecret(literal): %v", err)
	}
	if ref != "" || enc == "" || enc == "ghp_pasted_in" {
		t.Errorf("literal stored as enc=%q ref=%q, want an encrypted value and no reference", enc, ref)
	}

	// A reference to a secret that does not exist is rejected at save time, not
	// left to fail at the next pull.
	if _, _, err = s.storeSecret(1, secret.Ref("NOPE")); !errors.Is(err, ErrUnknownSecret) {
		t.Errorf("dangling reference error = %v, want ErrUnknownSecret", err)
	}
}

func TestSecretResolvesReferenceAtEveryUse(t *testing.T) {
	crypto.Init("test-master-key-for-registry-ref")
	vault := fakeVault{"GHCR_TOKEN": "ghp_live"}
	s := &Service{secrets: vault}

	reg := &models.Registry{WorkspaceID: 1, Name: "ghcr", SecretRef: "GHCR_TOKEN"}
	got, err := s.Secret(reg)
	if err != nil || got != "ghp_live" {
		t.Fatalf("Secret() = (%q, %v), want the current vault value", got, err)
	}

	// The credential holds no copy, so rotating the secret rotates it.
	vault["GHCR_TOKEN"] = "ghp_rotated"
	if got, _ = s.Secret(reg); got != "ghp_rotated" {
		t.Errorf("Secret() = %q after rotation, want ghp_rotated", got)
	}

	// A literal credential still resolves through the same call.
	enc, _ := crypto.EncryptWS(1, "ghp_literal")
	if got, err = s.Secret(&models.Registry{WorkspaceID: 1, Name: "dh", Secret: enc}); err != nil || got != "ghp_literal" {
		t.Errorf("Secret(literal) = (%q, %v)", got, err)
	}
}

// A vault-backed credential is identified by its reference, so rotating the
// secret behind it is not drift in a declarative plan — the next pull simply
// reads the new value. Pointing it at a different secret is a real change.
func TestFingerprintOfAReferenceTracksTheNameNotTheValue(t *testing.T) {
	a := Fingerprint("ghcr", secret.Ref("GHCR_TOKEN"))
	if a != Fingerprint("ghcr", secret.Ref("GHCR_TOKEN")) {
		t.Error("the same reference must fingerprint the same")
	}
	if a == Fingerprint("ghcr", secret.Ref("OTHER_TOKEN")) {
		t.Error("a different referenced secret must change the fingerprint")
	}
	if a == Fingerprint("ghcr", "ghp_literal") {
		t.Error("a reference and a literal must not collide")
	}
}
