// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package route

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestHostUnderDomain(t *testing.T) {
	domains := []models.Domain{{Name: "example.com"}, {Name: "acme.io"}}
	cases := []struct {
		host string
		want bool
	}{
		{"example.com", true},           // apex
		{"shop.example.com", true},      // subdomain
		{"a.b.example.com", true},       // nested subdomain
		{"acme.io", true},               // other registered apex
		{"notexample.com", false},       // suffix-but-not-subdomain must not match
		{"example.com.evil.com", false}, // domain as a label, not the parent
		{"other.org", false},            // unregistered
	}
	for _, c := range cases {
		if got := hostUnderDomain(c.host, domains); got != c.want {
			t.Errorf("hostUnderDomain(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

type fakeDomains struct{ list []models.Domain }

func (f fakeDomains) ListByWorkspace(uint) ([]models.Domain, error) { return f.list, nil }

func TestValidateHosts(t *testing.T) {
	s := &Service{}
	// No registry wired → check disabled.
	if err := s.validateHosts(1, []string{"anything.com"}); err != nil {
		t.Errorf("nil registry should skip: %v", err)
	}
	s.SetDomains(fakeDomains{list: []models.Domain{{Name: "example.com"}}})

	// validateHosts only checks that supplied hosts are registered; presence is
	// enforced separately in Create/Update (ErrHostRequired), so empty passes here.
	if err := s.validateHosts(1, nil); err != nil {
		t.Errorf("empty hosts should pass: %v", err)
	}
	// Registered subdomain passes.
	if err := s.validateHosts(1, []string{"shop.example.com"}); err != nil {
		t.Errorf("registered host should pass: %v", err)
	}
	// Unregistered host is rejected.
	if err := s.validateHosts(1, []string{"shop.example.com", "other.org"}); err == nil {
		t.Error("unregistered host should fail")
	}
}

func TestMatchDomain(t *testing.T) {
	domains := []models.Domain{
		{Name: "example.com", Verified: true},
		{Name: "sub.example.com", Verified: false}, // more specific registration
	}
	// Most specific registered domain wins (sub.example.com over example.com).
	if d := matchDomain("a.sub.example.com", domains); d == nil || d.Name != "sub.example.com" {
		t.Errorf("matchDomain(a.sub.example.com) = %v, want sub.example.com", d)
	}
	if d := matchDomain("shop.example.com", domains); d == nil || d.Name != "example.com" {
		t.Errorf("matchDomain(shop.example.com) = %v, want example.com", d)
	}
	if d := matchDomain("other.org", domains); d != nil {
		t.Errorf("matchDomain(other.org) = %v, want nil", d)
	}
}

func TestRouteServeState(t *testing.T) {
	verified := []models.Domain{{Name: "example.com", Verified: true}}
	unverified := []models.Domain{{Name: "example.com", Verified: false}}
	banned := []models.Domain{{Name: "example.com", Verified: true, Banned: true}}
	host := []string{"shop.example.com"}

	// Enabled route under a verified domain → live and served.
	serve, status, reason := routeServeState(&models.Route{Enabled: true, Hosts: host}, verified, true, false)
	if !serve || status != models.RouteStatusLive || reason != "" {
		t.Errorf("verified route: serve=%v status=%q reason=%q, want served/live/empty", serve, status, reason)
	}

	// Enabled route under an unverified domain → offline, not served.
	serve, status, reason = routeServeState(&models.Route{Enabled: true, Hosts: host}, unverified, true, false)
	if serve || status != models.RouteStatusOffline || reason == "" {
		t.Errorf("unverified route: serve=%v status=%q reason=%q, want not-served/offline/reason", serve, status, reason)
	}

	// Privileged workspace waives verification → served even when unverified.
	serve, status, _ = routeServeState(&models.Route{Enabled: true, Hosts: host}, unverified, true, true)
	if !serve || status != models.RouteStatusLive {
		t.Errorf("privileged unverified route: serve=%v status=%q, want served/live", serve, status)
	}

	// A ban always blocks — even a privileged workspace.
	serve, status, reason = routeServeState(&models.Route{Enabled: true, Hosts: host}, banned, true, true)
	if serve || status != models.RouteStatusOffline || reason == "" {
		t.Errorf("banned route (privileged): serve=%v status=%q reason=%q, want not-served/offline/reason", serve, status, reason)
	}

	// Disabled route → offline regardless of domain verification.
	serve, status, reason = routeServeState(&models.Route{Enabled: false, Hosts: host}, verified, true, false)
	if serve || status != models.RouteStatusOffline || reason != "disabled" {
		t.Errorf("disabled route: serve=%v status=%q reason=%q, want not-served/offline/disabled", serve, status, reason)
	}

	// A hostless structured route is never served — it would match every request
	// on its path and swallow all traffic.
	serve, status, reason = routeServeState(&models.Route{Enabled: true}, verified, true, false)
	if serve || status != models.RouteStatusOffline || reason != "route has no hosts" {
		t.Errorf("hostless route: serve=%v status=%q reason=%q, want not-served/offline/\"route has no hosts\"", serve, status, reason)
	}

	// An advanced-config route declares its hosts in the raw YAML, so the
	// structured-host gate does not apply.
	serve, status, _ = routeServeState(&models.Route{Enabled: true, AdvancedConfig: "path: /"}, verified, true, false)
	if !serve || status != models.RouteStatusLive {
		t.Errorf("advanced route: serve=%v status=%q, want served/live", serve, status)
	}

	// Platform-generated external-access route goes live without a verified domain
	// (it serves over the platform base domain, not a workspace-registered one).
	serve, status, _ = routeServeState(&models.Route{Enabled: true, Generated: true, Hosts: []string{"app.apps.example.com"}}, unverified, true, false)
	if !serve || status != models.RouteStatusLive {
		t.Errorf("generated route: serve=%v status=%q, want served/live", serve, status)
	}

	// A disabled generated route is still offline.
	serve, status, _ = routeServeState(&models.Route{Enabled: false, Generated: true, Hosts: []string{"app.apps.example.com"}}, unverified, true, false)
	if serve || status != models.RouteStatusOffline {
		t.Errorf("disabled generated route: serve=%v status=%q, want not-served/offline", serve, status)
	}

	// Nil registry disables gating (serve when enabled).
	serve, status, _ = routeServeState(&models.Route{Enabled: true, Hosts: []string{"anything.net"}}, nil, false, false)
	if !serve || status != models.RouteStatusLive {
		t.Errorf("nil registry: serve=%v status=%q, want served/live", serve, status)
	}
}

func TestNormalizeHosts(t *testing.T) {
	got := normalizeHosts([]string{" App.Example.com ", "app.example.com", "", "B.com"})
	want := []string{"app.example.com", "b.com"}
	if len(got) != len(want) {
		t.Fatalf("normalizeHosts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("normalizeHosts[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalizePath(t *testing.T) {
	for in, want := range map[string]string{"": "/", "  ": "/", "/api": "/api", " /x ": "/x"} {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateAdvancedRejectsInlineCert(t *testing.T) {
	cases := []struct {
		name    string
		cfg     string
		wantErr error
	}{
		{"empty", "", nil},
		{"plain", "path: /\nmethods: [GET]", nil},
		{"tls provider only", "tls:\n  provider: acme", nil},
		{"tls certificate", "tls:\n  certificate:\n    cert: AAAA\n    key: BBBB", ErrAdvancedTLSCert},
		{"tls cert key", "tls:\n  cert: AAAA\n  key: BBBB", ErrAdvancedTLSCert},
		{"tls certFile", "tls:\n  certFile: /etc/x.pem", ErrAdvancedTLSCert},
		{"bad yaml", "tls: [unterminated", ErrInvalidYAML},
	}
	for _, c := range cases {
		err := validateAdvanced(c.cfg)
		if c.wantErr == nil && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if c.wantErr != nil && !errors.Is(err, c.wantErr) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.wantErr)
		}
	}
}
