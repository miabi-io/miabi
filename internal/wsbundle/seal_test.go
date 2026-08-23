// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package wsbundle

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

const goodPass = "correct-horse-42"

func testState() *State {
	return &State{
		Schema:    StateSchema,
		Workspace: Workspace{Name: "shop", DisplayName: "Shop"},
		Secrets:   []Secret{{Name: "stripe-key", Value: "sk_live_supersecret"}},
		Apps: []Application{{
			Name: "api", SourceType: "image", Image: "ghcr.io/acme/api", Tag: "v2",
			Env: []EnvVar{{Key: "TOKEN", Value: "t0ps3cret", IsSecret: true}},
		}},
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	sealed, err := Seal(testState(), goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	out, err := Open(sealed, goodPass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if out.Workspace.Name != "shop" || len(out.Secrets) != 1 || out.Secrets[0].Value != "sk_live_supersecret" {
		t.Fatalf("round trip lost state: %+v", out)
	}
	if len(out.Apps) != 1 || out.Apps[0].Env[0].Value != "t0ps3cret" {
		t.Fatalf("round trip lost app env: %+v", out.Apps)
	}
}

// The state file holds a workspace's whole vault. Nothing in it may be readable
// without the passphrase — not a secret value, not a name.
func TestSealedStateCarriesNoPlaintext(t *testing.T) {
	sealed, err := Seal(testState(), goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, plaintext := range []string{"sk_live_supersecret", "t0ps3cret", "stripe-key", "ghcr.io/acme/api"} {
		if bytes.Contains(sealed, []byte(plaintext)) {
			t.Fatalf("%q appears in the sealed state file", plaintext)
		}
	}
}

// A bundle is only a migration if the whole graph crosses. This holds every class — and every
// cross-reference between them — through a round trip, so a field added to the model and forgotten
// in the state document shows up here rather than as a resource that quietly did not travel.
func TestStateCarriesEveryResourceClass(t *testing.T) {
	in := &State{
		Schema:       StateSchema,
		Workspace:    Workspace{Name: "shop"},
		Registries:   []Registry{{Name: "ghcr", Server: "ghcr.io", Secret: "tok"}},
		GitRepos:     []GitRepository{{Name: "app-repo", URL: "git@github.com:acme/app.git", Secret: "key"}},
		DNSProviders: []DNSProvider{{Name: "cf", Type: "cloudflare", Credentials: `{"api_token":"t"}`}},
		Networks:     []Network{{Name: "internal", Driver: "bridge", Internal: true}},
		Secrets:      []Secret{{Name: "stripe", Value: "sk"}},
		Configs:      []Config{{Name: "nginx", Data: map[string]string{"nginx.conf": "server {}"}}},
		Volumes:      []Volume{{Name: "uploads", SizeBytes: 1 << 30}},
		Stacks: []Stack{{
			Name: "web", Env: []EnvVar{{Key: "SHARED", Value: "v", IsSecret: true}},
		}},
		Databases: []DatabaseInstance{{
			Name: "pg", Engine: "postgres", Version: "16",
			Databases: []LogicalDatabase{{Name: "orders", App: "api", EnvPrefix: "ORDERS"}},
		}},
		Certificates: []Certificate{
			{Name: "shop-tls", Source: "imported", CertPEM: "-----BEGIN CERTIFICATE-----", KeyPEM: "-----BEGIN KEY-----"},
			{Name: "wildcard", Source: "acme", DNSProvider: "cf", AutoRenew: true},
		},
		Apps: []Application{{
			Name: "api", Stack: "web", Registry: "ghcr", GitRepository: "app-repo",
			Mounts: []Mount{
				{Volume: "uploads", Path: "/data"},
				{Config: "nginx", Key: "nginx.conf", Path: "/etc/nginx/nginx.conf"},
			},
		}},
		CronJobs:     []CronJob{{Name: "nightly", App: "api", Schedule: "0 2 * * *", Command: []string{"rake"}}},
		Middlewares:  []Middleware{{Name: "auth", Type: "basicAuth", Rule: map[string]any{"users": "admin:pw"}}},
		Routes:       []Route{{Name: "shop", App: "api", Hosts: []string{"shop.example.com"}, Middlewares: []string{"auth"}, Certificate: "shop-tls"}},
		Domains:      []Domain{{Name: "example.com", DNSProvider: "cf", Wildcard: true}},
		Environments: []Environment{{Name: "prod", Rank: 2, GitSource: "manifests"}},
		Pipelines:    []Pipeline{{Name: "ci", App: "api", Spec: "kind: Pipeline", Enabled: true}},
		GitSources:   []GitSource{{Name: "manifests", RepoURL: "https://github.com/acme/ops", GitRepository: "app-repo", Prune: true}},
		Members:      []Member{{Email: "dev@acme.test", Role: "developer"}},
	}

	sealed, err := Seal(in, goodPass)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	out, err := Open(sealed, goodPass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	outV := reflect.ValueOf(*out)
	for i := 0; i < outV.NumField(); i++ {
		f := outV.Type().Field(i)
		if f.Type.Kind() != reflect.Slice {
			continue
		}
		if outV.Field(i).Len() == 0 {
			t.Errorf("State.%s did not survive the round trip — is it carried by collect and restore?", f.Name)
		}
	}
	if len(out.Certificates) != 2 {
		t.Fatalf("certificates = %d, want 2", len(out.Certificates))
	}

	// Every cross-reference is a name, and each one still resolves.
	if got := out.Routes[0]; got.App != "api" || got.Certificate != "shop-tls" || got.Middlewares[0] != "auth" {
		t.Errorf("route lost a reference: %+v", got)
	}
	if got := out.Apps[0]; got.Stack != "web" || got.Registry != "ghcr" ||
		got.GitRepository != "app-repo" || got.Mounts[0].Volume != "uploads" {
		t.Errorf("app lost a reference: %+v", got)
	}
	// A config mount references its config by name and selects one key.
	if got := out.Apps[0].Mounts[1]; got.Config != "nginx" || got.Key != "nginx.conf" {
		t.Errorf("app lost its config mount: %+v", got)
	}
	if out.Configs[0].Data["nginx.conf"] != "server {}" {
		t.Errorf("config contents did not survive: %+v", out.Configs[0])
	}
	if got := out.Databases[0].Databases[0]; got.App != "api" || got.EnvPrefix != "ORDERS" {
		t.Errorf("logical database lost its consumer: %+v", got)
	}
	if out.Domains[0].DNSProvider != "cf" || out.Certificates[1].DNSProvider != "cf" {
		t.Error("the DNS connection did not travel with the domain or the managed certificate")
	}
	if out.Environments[0].GitSource != "manifests" || out.GitSources[0].GitRepository != "app-repo" {
		t.Error("a GitOps reference did not survive")
	}
	if out.CronJobs[0].App != "api" || out.Stacks[0].Env[0].Value != "v" {
		t.Error("a schedule or a stack's shared environment did not survive")
	}
	if out.Pipelines[0].App != "api" || out.Pipelines[0].Spec == "" {
		t.Error("the pipeline definition did not survive")
	}
}

func TestOpenRejectsWrongPassphrase(t *testing.T) {
	sealed, _ := Seal(testState(), goodPass)
	if _, err := Open(sealed, "another-horse-42"); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("Open with a wrong passphrase = %v, want ErrBadPassphrase", err)
	}
}

// The header is authenticated, so a file whose framing was edited fails to open
// rather than decrypting to something plausible.
func TestOpenRejectsTampering(t *testing.T) {
	sealed, _ := Seal(testState(), goodPass)

	header := append([]byte(nil), sealed...)
	header[len(magic)+2]++ // flip a salt byte
	if _, err := Open(header, goodPass); err == nil {
		t.Fatal("opened a file whose header was edited")
	}

	body := append([]byte(nil), sealed...)
	body[len(body)-1]++ // flip a ciphertext byte
	if _, err := Open(body, goodPass); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("Open of tampered ciphertext = %v, want ErrBadPassphrase", err)
	}

	if _, err := Open([]byte("not a bundle at all"), goodPass); !errors.Is(err, ErrNotState) {
		t.Fatalf("Open of foreign bytes = %v, want ErrNotState", err)
	}
}

// Sealing twice must not produce the same bytes: identical ciphertext would leak
// that a bundle did not change between two backups.
func TestSealIsNotDeterministic(t *testing.T) {
	a, _ := Seal(testState(), goodPass)
	b, _ := Seal(testState(), goodPass)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same state produced identical bytes")
	}
}

func TestWeakPassphrasesAreRefused(t *testing.T) {
	for _, pass := range []string{"", "short1!", "alllettersonly", "1234567890123"} {
		if err := ValidatePassphrase(pass); err == nil {
			t.Fatalf("ValidatePassphrase(%q) accepted a weak passphrase", pass)
		}
		if _, err := Seal(testState(), pass); err == nil {
			t.Fatalf("Seal accepted the weak passphrase %q", pass)
		}
	}
	if err := ValidatePassphrase(goodPass); err != nil {
		t.Fatalf("ValidatePassphrase(%q) = %v", goodPass, err)
	}
}

// A state document from a future schema is refused rather than half-understood.
func TestOpenRejectsUnknownSchema(t *testing.T) {
	st := testState()
	sealed, _ := Seal(st, goodPass)
	out, err := Open(sealed, goodPass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	out.Schema = StateSchema + 1
	if err := out.Validate(); err == nil {
		t.Fatal("a state document from a newer schema validated")
	}
}
