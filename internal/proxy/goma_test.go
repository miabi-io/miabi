// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderACMEOmitsTLS(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID: 1, WorkspaceID: 1, Name: "web", Hosts: []string{"app.example.com"},
		Backends: []Backend{{Endpoint: "http://mb-app-1:8080"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"routes:", "name: mb-ws1-web", "app.example.com", "endpoint: http://mb-app-1:8080"} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered route missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "tls:") {
		t.Errorf("ACME route must not contain a tls block:\n%s", s)
	}
}

func TestRenderTLSNoneSetsProviderNone(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID: 1, WorkspaceID: 1, Name: "web", Hosts: []string{"app.example.com"},
		Backends: []Backend{{Endpoint: "http://mb-app-1:8080"}},
		TLSNone:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "tls:") || !strings.Contains(s, "provider: none") {
		t.Errorf("TLS-none route must set tls.provider: none:\n%s", s)
	}
}

func TestRenderDisabledRouteSetsEnabledFalse(t *testing.T) {
	base := RenderedRoute{
		ID: 1, WorkspaceID: 1, Name: "web", Hosts: []string{"app.example.com"},
		Backends: []Backend{{Endpoint: "http://mb-app-1:8080"}},
	}
	// Enabled route omits the key (Goma defaults to true).
	out, err := RenderRoute(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "enabled:") {
		t.Errorf("enabled route must omit the enabled key:\n%s", out)
	}
	// Disabled route renders enabled: false.
	base.Disabled = true
	out, err = RenderRoute(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "enabled: false") {
		t.Errorf("disabled route must render enabled: false:\n%s", out)
	}
}

func TestRenderCustomInlinesPEM(t *testing.T) {
	certPEM := "-----BEGIN CERTIFICATE-----\nMII...\n-----END CERTIFICATE-----"
	keyPEM := "-----BEGIN PRIVATE KEY-----\nMII...\n-----END PRIVATE KEY-----"
	out, err := RenderRoute(RenderedRoute{
		ID: 2, Name: "api", Hosts: []string{"api.example.com"},
		Backends: []Backend{{Endpoint: "http://mb-app-2:3000"}},
		Certs:    []CertPair{{CertPEM: certPEM, KeyPEM: keyPEM}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "tls:") || !strings.Contains(s, "certificate:") {
		t.Errorf("custom route must contain tls.certificate:\n%s", s)
	}
	// Cert/key are base64-encoded, not raw PEM.
	if strings.Contains(s, "BEGIN CERTIFICATE") || strings.Contains(s, "BEGIN PRIVATE KEY") {
		t.Errorf("cert/key must be base64-encoded, not raw PEM:\n%s", s)
	}
	if !strings.Contains(s, base64.StdEncoding.EncodeToString([]byte(certPEM))) ||
		!strings.Contains(s, base64.StdEncoding.EncodeToString([]byte(keyPEM))) {
		t.Errorf("custom route must inline base64-encoded cert and key:\n%s", s)
	}
}

func TestRenderWeightedBackends(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID: 9, Name: "canary", Hosts: []string{"app.example.com"},
		Backends: []Backend{
			{Endpoint: "http://mb-app-9:80", Weight: 90},
			{Endpoint: "http://mb-app-9-canary:80", Weight: 10},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"endpoint: http://mb-app-9:80", "weight: 90", "endpoint: http://mb-app-9-canary:80", "weight: 10"} {
		if !strings.Contains(s, want) {
			t.Errorf("weighted route missing %q:\n%s", want, s)
		}
	}
}

func TestRenderHTTPSBackendSkipsTLSVerify(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID: 7, Name: "secure", Hosts: []string{"app.example.com"},
		Backends: []Backend{{Endpoint: "https://mb-app-7:8443"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"endpoint: https://mb-app-7:8443", "security:", "insecureSkipVerify: true"} {
		if !strings.Contains(s, want) {
			t.Errorf("https route missing %q:\n%s", want, s)
		}
	}
}

func TestRenderHTTPBackendOmitsSecurity(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID: 8, Name: "plain", Hosts: []string{"app.example.com"},
		Backends: []Backend{{Endpoint: "http://mb-app-8:80"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "security:") {
		t.Errorf("http route should not emit a security block:\n%s", out)
	}
}

func TestRenderAdvancedInjectsNameAndBackends(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID:          12,
		WorkspaceID: 1,
		Name:        "adv",
		// User-authored config; note a hand-typed (wrong) target that must be ignored.
		AdvancedYAML: "path: /api\nhosts: [api.example.com]\nrewrite: /v2\ntarget: http://evil:9999\nmiddlewares: [basic-auth]\n",
		Backends:     []Backend{{Endpoint: "http://mb-app-12:80"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"name: mb-ws1-adv", "path: /api", "rewrite: /v2", "endpoint: http://mb-app-12:80", "mb-ws1-basic-auth"} {
		if !strings.Contains(s, want) {
			t.Errorf("advanced route missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "evil") {
		t.Errorf("advanced route leaked the hand-typed target:\n%s", s)
	}
}

func TestRenderMiddleware(t *testing.T) {
	out, err := RenderMiddleware(RenderedMiddleware{
		ID: 3, WorkspaceID: 1, Name: "auth", Type: "basicAuth", Paths: []string{"/*"},
		Rule: map[string]interface{}{"realm": "restricted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"middlewares:", "name: mb-ws1-auth", "type: basicAuth", "realm: restricted"} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered middleware missing %q:\n%s", want, s)
		}
	}
}

func TestRenderHostlessRouteForcedDisabled(t *testing.T) {
	// A structured route with no host would match every request on its path, so the
	// renderer must never emit it as live — enabled: false is forced.
	out, err := RenderRoute(RenderedRoute{
		ID: 1, WorkspaceID: 5, Name: "test", Path: "/",
		Backends: []Backend{{Endpoint: "http://mb-app-1:80"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "enabled: false") {
		t.Errorf("hostless route must render enabled: false:\n%s", out)
	}
}

// TestRenderPhase1Middlewares checks the transform/security types added in the
// middleware-UI expansion render the exact rule keys Goma's structs expect. The
// mirror structs below carry the same yaml tags as goma-gateway's
// internal/types.go (RedirectRule/RedirectRegexRule/AddPrefixRule/UserAgentBlock),
// so a successful unmarshal into a non-zero struct proves key parity — a renamed
// key would round-trip to a zero field and fail the assertion.
func TestRenderPhase1Middlewares(t *testing.T) {
	type redirectRule struct {
		URL       string `yaml:"url"`
		Permanent bool   `yaml:"permanent"`
	}
	type redirectRegexRule struct {
		Pattern     string `yaml:"pattern"`
		Replacement string `yaml:"replacement"`
		Permanent   bool   `yaml:"permanent"`
	}
	type addPrefixRule struct {
		Prefix string `yaml:"prefix"`
	}
	type userAgentBlockRule struct {
		UserAgents []string `yaml:"userAgents"`
	}

	assertRule := func(t *testing.T, mwType string, rule map[string]any, into any, check func()) {
		t.Helper()
		out, err := RenderMiddleware(RenderedMiddleware{ID: 1, WorkspaceID: 2, Name: "mw", Type: mwType, Rule: rule})
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Middlewares []struct {
				Rule yaml.Node `yaml:"rule"`
			} `yaml:"middlewares"`
		}
		if err := yaml.Unmarshal(out, &doc); err != nil {
			t.Fatalf("unmarshal rendered %s: %v\n%s", mwType, err, out)
		}
		if len(doc.Middlewares) != 1 {
			t.Fatalf("expected 1 middleware, got %d:\n%s", len(doc.Middlewares), out)
		}
		if err := doc.Middlewares[0].Rule.Decode(into); err != nil {
			t.Fatalf("decode %s rule into goma struct: %v\n%s", mwType, err, out)
		}
		check()
	}

	var rd redirectRule
	assertRule(t, "redirect", map[string]any{"url": "https://example.com", "permanent": true}, &rd, func() {
		if rd.URL != "https://example.com" || !rd.Permanent {
			t.Fatalf("redirect rule mismatch: %+v", rd)
		}
	})
	var rr redirectRegexRule
	assertRule(t, "redirectRegex", map[string]any{"pattern": "^/o/(.*)", "replacement": "/n/$1"}, &rr, func() {
		if rr.Pattern != "^/o/(.*)" || rr.Replacement != "/n/$1" {
			t.Fatalf("redirectRegex rule mismatch: %+v", rr)
		}
	})
	var ap addPrefixRule
	assertRule(t, "addPrefix", map[string]any{"prefix": "/api"}, &ap, func() {
		if ap.Prefix != "/api" {
			t.Fatalf("addPrefix rule mismatch: %+v", ap)
		}
	})
	var ua userAgentBlockRule
	assertRule(t, "userAgentBlock", map[string]any{"userAgents": []any{"curl", "Googlebot"}}, &ua, func() {
		if len(ua.UserAgents) != 2 || ua.UserAgents[0] != "curl" {
			t.Fatalf("userAgentBlock rule mismatch: %+v", ua)
		}
	})
}

// TestRenderHeaderAndErrorMiddlewares checks the map/list rule shapes (Phase 2/3)
// render into the keys Goma's RequestHeader / ResponseHeader / RouteErrorInterceptor
// structs expect — the nested maps and object lists round-trip through YAML intact.
func TestRenderHeaderAndErrorMiddlewares(t *testing.T) {
	decode := func(t *testing.T, mwType string, rule map[string]any, into any) {
		t.Helper()
		out, err := RenderMiddleware(RenderedMiddleware{ID: 1, WorkspaceID: 2, Name: "mw", Type: mwType, Rule: rule})
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Middlewares []struct {
				Rule yaml.Node `yaml:"rule"`
			} `yaml:"middlewares"`
		}
		if err := yaml.Unmarshal(out, &doc); err != nil {
			t.Fatalf("unmarshal %s: %v\n%s", mwType, err, out)
		}
		if err := doc.Middlewares[0].Rule.Decode(into); err != nil {
			t.Fatalf("decode %s: %v\n%s", mwType, err, out)
		}
	}

	var reqH struct {
		SetHeaders    map[string]string `yaml:"setHeaders"`
		RemoveHeaders []string          `yaml:"removeHeaders"`
	}
	decode(t, "requestHeaders", map[string]any{
		"setHeaders":    map[string]any{"X-Forwarded-Proto": "https"},
		"removeHeaders": []any{"Authorization"},
	}, &reqH)
	if reqH.SetHeaders["X-Forwarded-Proto"] != "https" || len(reqH.RemoveHeaders) != 1 {
		t.Fatalf("requestHeaders mismatch: %+v", reqH)
	}

	var errI struct {
		Enabled bool `yaml:"enabled"`
		Errors  []struct {
			StatusCode int    `yaml:"statusCode"`
			Body       string `yaml:"body"`
		} `yaml:"errors"`
	}
	decode(t, "errorInterceptor", map[string]any{
		"enabled": true,
		"errors":  []any{map[string]any{"statusCode": 404, "body": "gone"}},
	}, &errI)
	if !errI.Enabled || len(errI.Errors) != 1 || errI.Errors[0].StatusCode != 404 || errI.Errors[0].Body != "gone" {
		t.Fatalf("errorInterceptor mismatch: %+v", errI)
	}
}

func TestRenderMiddlewareDefaultsPaths(t *testing.T) {
	// A middleware with no paths must still render an explicit paths field
	// (defaulting to /*), otherwise Goma silently applies it to nothing.
	out, err := RenderMiddleware(RenderedMiddleware{
		ID: 4, WorkspaceID: 5, Name: "access", Type: "access",
		Rule: map[string]interface{}{"statusCode": 403},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "paths:") || !strings.Contains(s, "/*") {
		t.Errorf("middleware without paths must default to /*:\n%s", s)
	}
}

func TestRenderNamespacesNamesAndRefs(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID: 5, WorkspaceID: 2, Name: "My API", Hosts: []string{"api.example.com"},
		Middlewares: []string{"Basic Auth"},
		Backends:    []Backend{{Endpoint: "http://mb-app-5:80"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// Route name and middleware reference are workspace-namespaced and slugified.
	for _, want := range []string{"name: mb-ws2-my-api", "mb-ws2-basic-auth"} {
		if !strings.Contains(s, want) {
			t.Errorf("namespaced route missing %q:\n%s", want, s)
		}
	}
}

func TestGomaSyncWorkspaceWritesOneFile(t *testing.T) {
	dir := t.TempDir()
	g := NewGoma(dir)
	ctx := context.Background()

	// A workspace with one route referencing one middleware lands in a single file.
	routes := []RenderedRoute{{ID: 7, WorkspaceID: 3, Name: "svc", Hosts: []string{"svc.example.com"}, Middlewares: []string{"auth"}, Backends: []Backend{{Endpoint: "http://mb-app-7:80"}}}}
	mws := []RenderedMiddleware{{ID: 9, WorkspaceID: 3, Name: "auth", Type: "basicAuth", Rule: map[string]interface{}{"username": "u"}}}
	if err := g.SyncWorkspace(ctx, 3, routes, mws); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mb-ws3.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected workspace file at %s: %v", path, err)
	}
	// The route and the middleware definition it references must be in the same
	// file, with the route's reference namespaced to the middleware's Goma name.
	s := string(body)
	for _, want := range []string{"mb-ws3-svc", "mb-ws3-auth", "basicAuth"} {
		if !strings.Contains(s, want) {
			t.Fatalf("workspace file missing %q:\n%s", want, s)
		}
	}

	// An empty sync removes the workspace file (idempotent).
	if err := g.SyncWorkspace(ctx, 3, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected workspace file removed, stat err = %v", err)
	}
	if err := g.SyncWorkspace(ctx, 3, nil, nil); err != nil {
		t.Fatalf("removing missing workspace file should be a no-op, got %v", err)
	}
}

func TestSyncRegistryWritesAndRemoves(t *testing.T) {
	dir := t.TempDir()
	g := NewGoma(dir)
	path := filepath.Join(dir, "mb-registry.yml")

	cfg := RegistryProxy{
		Enabled:     true,
		Host:        "registry.example.com",
		Upstream:    "http://mb-registry:5000",
		AuthURL:     "http://miabi:9000/internal/registry/auth",
		TLSProvider: "wildcard",
	}
	if err := g.SyncRegistry(context.Background(), cfg); err != nil {
		t.Fatalf("SyncRegistry: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out := string(b)
	for _, want := range []string{
		"registry.example.com",
		"http://mb-registry:5000",
		"mb-registry-https",
		"redirectScheme",
		"mb-registry-auth",
		"forwardAuth",
		"http://miabi:9000/internal/registry/auth",
		"X-Miabi-Registry-Namespace",
		"mb-registry-ns-rewrite",
		"rewriteRegex",
		"^/v2/[^/]+/(.*)",
		"/v2/{{goma.headers.X-Miabi-Registry-Namespace}}/$1",
		"provider: wildcard",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered registry config missing %q\n%s", want, out)
		}
	}

	// Disabled (or incomplete) config removes the file.
	if err := g.SyncRegistry(context.Background(), RegistryProxy{Enabled: false}); err != nil {
		t.Fatalf("SyncRegistry disable: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected registry file removed, stat err = %v", err)
	}
}
