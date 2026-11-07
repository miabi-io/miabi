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

package gitrepo

import (
	"errors"
	"strings"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
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

func TestAuthForResolvesVaultReference(t *testing.T) {
	crypto.Init("test-master-key-for-gitrepo-ref")
	vault := fakeVault{"GH_TOKEN": "ghp_from_the_vault"}

	g := &models.GitRepository{
		Name: "acme-api", AuthType: models.GitAuthToken,
		Username: "x-access-token", SecretRef: "GH_TOKEN",
	}
	auth, err := AuthFor(g, vault)
	if err != nil {
		t.Fatalf("AuthFor: %v", err)
	}
	basic, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("want basic auth, got %T", auth)
	}
	if basic.Password != "ghp_from_the_vault" {
		t.Errorf("password = %q, want the current vault value", basic.Password)
	}

	// The credential holds no copy, so rotating the secret rotates it with no edit.
	vault["GH_TOKEN"] = "ghp_rotated"
	auth, _ = AuthFor(g, vault)
	if basic := auth.(*githttp.BasicAuth); basic.Password != "ghp_rotated" {
		t.Errorf("password = %q, want the rotated value", basic.Password)
	}
}

// An unresolvable reference must fail loudly; anonymous cloning would surface later as "repository not found".
func TestAuthForFailsOnUnresolvableReference(t *testing.T) {
	g := &models.GitRepository{Name: "acme-api", AuthType: models.GitAuthToken, SecretRef: "GONE"}

	if _, err := AuthFor(g, fakeVault{}); err == nil {
		t.Error("a reference to a missing secret must be an error, not an anonymous clone")
	}
	_, err := AuthFor(g, nil)
	if err == nil || !strings.Contains(err.Error(), "vault is unavailable") {
		t.Errorf("a reference with no vault wired must error, got %v", err)
	}
}

func TestCredentialURLResolvesVaultReference(t *testing.T) {
	crypto.Init("test-master-key-for-gitrepo-ref")
	vault := fakeVault{"GH_TOKEN": "ghp_from_the_vault"}

	g := &models.GitRepository{Name: "acme-api", AuthType: models.GitAuthToken, SecretRef: "GH_TOKEN"}
	got, err := CredentialURL("https://github.com/acme/api.git", g, vault)
	if err != nil {
		t.Fatalf("CredentialURL: %v", err)
	}
	if got != "https://x-access-token:ghp_from_the_vault@github.com/acme/api.git" {
		t.Errorf("URL = %q", got)
	}
}

// A public repo never authenticates, whichever form its (leftover) secret takes.
func TestPublicRepoIgnoresCredential(t *testing.T) {
	g := &models.GitRepository{AuthType: models.GitAuthPublic, SecretRef: "GH_TOKEN"}
	auth, err := AuthFor(g, fakeVault{})
	if err != nil || auth != nil {
		t.Errorf("public repo: got auth %v err %v, want anonymous", auth, err)
	}
}
