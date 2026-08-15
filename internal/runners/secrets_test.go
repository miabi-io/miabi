// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/miabi-io/runner/proto"
)

// fakeVault resolves the references it knows and errors on the rest, like
// secret.Service.ResolveAll.
type fakeVault struct {
	values map[string]string
	calls  int
}

func (f *fakeVault) ResolveAll(_ uint, env []string) ([]string, error) {
	f.calls++
	out := make([]string, len(env))
	for i, e := range env {
		out[i] = e
		for name, val := range f.values {
			out[i] = strings.ReplaceAll(out[i], "${{ secrets."+name+" }}", val)
		}
		if secretRef.MatchString(out[i]) {
			return nil, fmt.Errorf("secret %q is not defined in this workspace", refName(out[i]))
		}
	}
	return out, nil
}

func refName(entry string) string {
	m := secretRef.FindString(entry)
	m = strings.TrimPrefix(m, "${{")
	m = strings.TrimSuffix(m, "}}")
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m), "secrets."))
}

func specWith(jobEnv []string, stepEnv []string) *proto.JobSpec {
	return &proto.JobSpec{
		Env:   jobEnv,
		Steps: []proto.StepSpec{{Ordinal: 0, Name: "test", Env: stepEnv}},
	}
}

func TestResolveJobSecrets(t *testing.T) {
	vault := &fakeVault{values: map[string]string{"NPM_TOKEN": "npm_s3cret", "DSN": "postgres://u:p@h/db"}}
	spec := specWith(
		[]string{"NODE_ENV=production", "NPM_TOKEN=${{ secrets.NPM_TOKEN }}"},
		[]string{"CI=true", "DATABASE_URL=${{ secrets.DSN }}"},
	)

	mask, err := resolveJobSecrets(vault, 1, spec)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := spec.Env[1]; got != "NPM_TOKEN=npm_s3cret" {
		t.Errorf("job env = %q", got)
	}
	if got := spec.Steps[0].Env[1]; got != "DATABASE_URL=postgres://u:p@h/db" {
		t.Errorf("step env = %q", got)
	}
	// Only what a reference expanded to is masked; literals are not.
	want := map[string]bool{"npm_s3cret": true, "postgres://u:p@h/db": true}
	if len(mask) != len(want) {
		t.Fatalf("mask = %v, want %d values", mask, len(want))
	}
	for _, v := range mask {
		if !want[v] {
			t.Errorf("unexpected value in the redaction set: %q", v)
		}
	}
}

// A run must not start with a blank where a credential belongs.
func TestResolveJobSecretsFailsOnMissing(t *testing.T) {
	vault := &fakeVault{values: map[string]string{}}
	spec := specWith([]string{"TOKEN=${{ secrets.NOPE }}"}, nil)

	_, err := resolveJobSecrets(vault, 1, spec)
	if err == nil {
		t.Fatal("expected an error for an undefined secret")
	}
	if !strings.Contains(err.Error(), "NOPE") {
		t.Errorf("error should name the secret: %v", err)
	}
}

func TestResolveJobSecretsFailsOnStepMiss(t *testing.T) {
	vault := &fakeVault{values: map[string]string{}}
	spec := specWith(nil, []string{"TOKEN=${{ secrets.NOPE }}"})

	_, err := resolveJobSecrets(vault, 1, spec)
	if err == nil || !strings.Contains(err.Error(), `step "test"`) {
		t.Fatalf("the failing step should be named: %v", err)
	}
}

// Sending the literal reference to a runner would be worse than failing: the
// step would run with a nonsense value and nothing would say why.
func TestResolveJobSecretsWithoutAVault(t *testing.T) {
	spec := specWith([]string{"TOKEN=${{ secrets.X }}"}, nil)
	if _, err := resolveJobSecrets(nil, 1, spec); !errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("err = %v, want ErrSecretsUnavailable", err)
	}
	if spec.Env[0] != "TOKEN=${{ secrets.X }}" {
		t.Error("the spec was mutated on a failed resolve")
	}
}

// No references means no vault round trip at all.
func TestResolveJobSecretsSkipsWhenNoReferences(t *testing.T) {
	vault := &fakeVault{values: map[string]string{"X": "y"}}
	spec := specWith([]string{"NODE_ENV=production"}, []string{"CI=true"})

	mask, err := resolveJobSecrets(vault, 1, spec)
	if err != nil || mask != nil {
		t.Fatalf("mask = %v, err = %v", mask, err)
	}
	if vault.calls != 0 {
		t.Errorf("vault was consulted %d times for a job with no references", vault.calls)
	}
}

func TestChangedValuesIgnoresUntouchedEntries(t *testing.T) {
	before := []string{"A=1", "B=${{ secrets.S }}"}
	after := []string{"A=1", "B=resolved"}
	got := changedValues(before, after)
	if len(got) != 1 || got[0] != "resolved" {
		t.Fatalf("changedValues = %v", got)
	}
}

func TestValueOf(t *testing.T) {
	// A value containing '=' must survive intact — a token or DSN routinely does.
	if got := valueOf("DSN=postgres://u:p@h/db?x=1"); got != "postgres://u:p@h/db?x=1" {
		t.Errorf("got %q", got)
	}
	if got := valueOf("EMPTY="); got != "" {
		t.Errorf("got %q", got)
	}
	if got := valueOf("nonsense"); got != "" {
		t.Errorf("got %q", got)
	}
}
