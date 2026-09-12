// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/config"
)

// refService builds a service whose registry resolves to host, backed by the
// acme(7)/other(8) workspace fixture. No settings store: enablement and the host
// are environment-derived, which is the whole point of the lock.
func refService(host string) *Service {
	return &Service{cfg: config.RegistryConfig{Enabled: true, Host: host}, ws: wsFixture()}
}

func TestResolveImageRef(t *testing.T) {
	const host = "registry.example.com"
	svc := refService(host)

	cases := []struct {
		name      string
		workspace uint
		ref       string
		want      string
		wantErr   error
	}{
		// The vulnerability: workspace 7 naming workspace 8's image. Both the
		// id form and the name form must be refused — an attacker reads the
		// namespace straight off the other workspace's Connect tab.
		{"foreign namespace by id", 7, host + "/ws_8/api:latest", "", ErrForeignNamespace},
		{"foreign namespace by name", 7, host + "/other/api:latest", "", ErrForeignNamespace},
		{"foreign namespace by digest", 7, host + "/ws_8/api@sha256:abc", "", ErrForeignNamespace},
		{"foreign nested repository", 7, host + "/other/team/api:1", "", ErrForeignNamespace},

		// Own namespace, in either form, canonicalized to ws_<id>.
		{"own namespace by id", 7, host + "/ws_7/api:latest", host + "/ws_7/api:latest", nil},
		{"own namespace by name", 7, host + "/acme/api:latest", host + "/ws_7/api:latest", nil},
		{"own nested repository", 7, host + "/acme/team/api:1", host + "/ws_7/team/api:1", nil},
		{"own namespace by digest", 7, host + "/acme/api@sha256:abc", host + "/ws_7/api@sha256:abc", nil},

		// A namespace nobody owns is refused rather than passed through: it can be
		// claimed by a rename, which would turn it into a foreign one silently.
		{"unknown namespace", 7, host + "/ghost/api:1", "", ErrUnknownNamespace},
		{"no namespace segment", 7, host + "/api:1", "", ErrUnknownNamespace},

		// External images are none of this check's business.
		{"docker hub", 7, "nginx:1.27", "nginx:1.27", nil},
		{"other registry", 7, "ghcr.io/acme/api:1", "ghcr.io/acme/api:1", nil},
		{"host as a prefix of another host", 7, host + ".evil.test/ws_8/api:1", host + ".evil.test/ws_8/api:1", nil},
		{"empty ref", 7, "", "", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.ResolveImageRef(tc.workspace, tc.ref)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ref = %q, want %q", got, tc.want)
			}
		})
	}
}

// Without a workspace finder the ownership of a namespace cannot be established,
// so an internal reference must fail closed rather than be waved through.
func TestResolveImageRefFailsClosedWithoutWorkspaceFinder(t *testing.T) {
	svc := &Service{cfg: config.RegistryConfig{Enabled: true, Host: "registry.example.com"}}
	if _, err := svc.ResolveImageRef(7, "registry.example.com/ws_7/api:1"); err == nil {
		t.Fatal("an internal ref resolved with no workspace finder wired; it must be refused")
	}
	if got, err := svc.ResolveImageRef(7, "nginx:1"); err != nil || got != "nginx:1" {
		t.Fatalf("external ref = (%q,%v), want (nginx:1,<nil>)", got, err)
	}
}

// With no registry host there is no internal registry to cross into, so every
// reference is external and passes through.
func TestResolveImageRefWithoutHost(t *testing.T) {
	svc := &Service{cfg: config.RegistryConfig{}, ws: wsFixture()}
	got, err := svc.ResolveImageRef(7, "registry.example.com/ws_8/api:1")
	if err != nil || got != "registry.example.com/ws_8/api:1" {
		t.Fatalf("ref = (%q,%v), want the input unchanged", got, err)
	}
}

func TestValidateImageRef(t *testing.T) {
	svc := refService("registry.example.com")
	if err := svc.ValidateImageRef(7, "registry.example.com/acme/api:1"); err != nil {
		t.Errorf("own image rejected: %v", err)
	}
	if err := svc.ValidateImageRef(7, "registry.example.com/other/api:1"); !errors.Is(err, ErrForeignNamespace) {
		t.Errorf("foreign image = %v, want ErrForeignNamespace", err)
	}
}

func TestBuildRefUsesImmutableNamespace(t *testing.T) {
	svc := refService("registry.example.com")
	// The name form would break on a rename; the recorded ref must be id-based.
	if got, want := svc.BuildRef(7, "api", 42), "registry.example.com/ws_7/api:42"; got != want {
		t.Errorf("BuildRef = %q, want %q", got, want)
	}
	// And it round-trips through the ownership check unchanged.
	resolved, err := svc.ResolveImageRef(7, svc.BuildRef(7, "api", 42))
	if err != nil || resolved != "registry.example.com/ws_7/api:42" {
		t.Errorf("BuildRef does not survive ResolveImageRef: (%q,%v)", resolved, err)
	}
}

func TestNormalizeHost(t *testing.T) {
	valid := map[string]string{
		"":                            "",
		"registry.example.com":        "registry.example.com",
		"  Registry.Example.COM  ":    "registry.example.com",
		"registry.example.com:5000":   "registry.example.com:5000",
		"localhost:5000":              "localhost:5000",
		"reg-1.sub.example.co.uk":     "reg-1.sub.example.co.uk",
		"registry.internal.test:8443": "registry.internal.test:8443",
	}
	for in, want := range valid {
		got, err := NormalizeHost(in)
		if err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = (%q,%v), want (%q,<nil>)", in, got, err, want)
		}
	}
	invalid := []string{
		"https://registry.example.com",  // scheme
		"registry.example.com/v2",       // path
		"registry.example.com/",         // trailing slash
		"*.example.com",                 // wildcard
		"registry",                      // single label reads as a Docker Hub namespace
		"-registry.example.com",         // leading hyphen
		"registry..example.com",         // empty label
		"registry.example.com:notaport", // bad port
		"registry example.com",          // space
	}
	for _, in := range invalid {
		if got, err := NormalizeHost(in); err == nil {
			t.Errorf("NormalizeHost(%q) = (%q,<nil>), want an error", in, got)
		}
	}
}

// A workspace name can never collide with the ws_<id> storage namespace, which
// is what makes resolving either form unambiguous.
func TestNamespaceFormsAreDisjoint(t *testing.T) {
	if _, ok := parseIDNamespace(Namespace(7)); !ok {
		t.Fatal("Namespace(7) must parse back as an id namespace")
	}
}
