// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"strings"
	"testing"
)

func testResolver(secrets, env map[string]string) Resolver {
	return ResolverFunc{
		SecretFn: func(n string) (string, bool) { v, ok := secrets[n]; return v, ok },
		EnvFn:    func(n string) (string, bool) { v, ok := env[n]; return v, ok },
	}
}

func TestResolveSubstitutesBothNamespaces(t *testing.T) {
	r := testResolver(
		map[string]string{"db_password": "s3cr3t"},
		map[string]string{"DATABASE_HOST": "db.internal", "PORT": "8080"},
	)

	content := "host=${{ env.DATABASE_HOST }}\nport=${{env.PORT}}\npassword=${{ secrets.db_password }}\n"
	got, err := Resolve(content, r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := "host=db.internal\nport=8080\npassword=s3cr3t\n"
	if got != want {
		t.Errorf("Resolve =\n%q\nwant\n%q", got, want)
	}
}

// A secret of the same name wins, so promoting a value into the vault needs no
// edit to the config that reads it.
func TestSecretTakesPriorityOverEnv(t *testing.T) {
	r := testResolver(
		map[string]string{"API_KEY": "from-vault"},
		map[string]string{"API_KEY": "from-env"},
	)

	for _, ref := range []string{"${{ secrets.API_KEY }}", "${{ env.API_KEY }}"} {
		got, err := Resolve(ref, r)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", ref, err)
		}
		if got != "from-vault" {
			t.Errorf("Resolve(%s) = %q, want the vault value", ref, got)
		}
	}
}

func TestResolveFailsOnUnknownReference(t *testing.T) {
	r := testResolver(map[string]string{}, map[string]string{})

	for _, ref := range []string{"${{ secrets.nope }}", "${{ env.NOPE }}"} {
		if _, err := Resolve(ref, r); !errors.Is(err, ErrUnresolvedRef) {
			t.Errorf("Resolve(%s) = %v, want ErrUnresolvedRef", ref, err)
		}
	}

	// An env-namespaced name never falls back to a secret it does not have.
	_, err := Resolve("${{ secrets.known }} ${{ env.MISSING }}", testResolver(
		map[string]string{"known": "v"}, map[string]string{}))
	if !errors.Is(err, ErrUnresolvedRef) || !strings.Contains(err.Error(), "env.MISSING") {
		t.Errorf("error = %v, want it to name env.MISSING", err)
	}
}

// A file with no references is returned untouched, newlines and all.
func TestResolveLeavesPlainContentAlone(t *testing.T) {
	const pem = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	got, err := Resolve(pem, testResolver(nil, nil))
	if err != nil || got != pem {
		t.Errorf("Resolve(plain) = %q, %v", got, err)
	}
	if got, err := Resolve(pem, nil); err != nil || got != pem {
		t.Errorf("Resolve(no resolver) = %q, %v", got, err)
	}
}

// A resolved value containing $1 must not be re-expanded as a capture group.
func TestResolvedValueIsLiteral(t *testing.T) {
	r := testResolver(map[string]string{"pw": "p$1ss${word}"}, nil)
	got, err := Resolve("password=${{ secrets.pw }}", r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "password=p$1ss${word}" {
		t.Errorf("Resolve = %q, want the value literally", got)
	}
}

func TestSecretRefNames(t *testing.T) {
	data := map[string]string{
		"app.conf": "a=${{ secrets.alpha }}\nb=${{ env.BETA }}\n",
		"db.conf":  "c=${{secrets.gamma}}\nd=${{ secrets.alpha }}\n",
		"plain":    "nothing here",
	}
	got := SecretRefNames(data)
	want := []string{"alpha", "gamma"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("SecretRefNames = %v, want %v", got, want)
	}

	if HasReferences("plain text") {
		t.Error("HasReferences(plain) = true")
	}
	if !HasReferences("x=${{ env.Y }}") {
		t.Error("HasReferences(ref) = false")
	}
}
