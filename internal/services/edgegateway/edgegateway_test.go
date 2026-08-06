// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"context"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestRenderConfig(t *testing.T) {
	s := NewService(nil, "https://miabi.example.com/", "jkaninda/goma-gateway:latest", "miabi", "ops@example.com")
	got := s.RenderConfig(&models.Server{Name: "edge-1"})

	for _, want := range []string{
		"version: 2",
		// Default log verbosity under gateway.
		"log:",
		"level: info",
		// Multi-provider certManager schema: a default + named providers map.
		"defaultProvider: acme",
		"providers:",
		"type: acme",
		"email: ops@example.com",
		// DNS cache flushed on reload so re-created backends are re-resolved.
		"dnsCache:",
		"ttl: 300",
		"clearOnReload: true",
		// Trailing slash on the control URL must be trimmed (no double slash).
		"endpoint: https://miabi.example.com/api/v1/provider/edge-1",
		"enabled: true",
		// The token is referenced as an env var placeholder, not inlined.
		"Authorization: ${INSTANCE_API_KEY}",
		// Entry points on 80/443.
		"web:",
		"[::]:80",
		"[::]:443",
		"certsDir:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "//api/v1") {
		t.Errorf("endpoint has a double slash:\n%s", got)
	}
}

func TestRenderConfigManagerUsesFileProvider(t *testing.T) {
	// A stack install: Miabi created the volume behind its route directory, so it
	// can hand the manager's gateway the same one to watch.
	s := NewService(nil, "https://miabi.example.com/", "img", "miabi", "ops@example.com")
	s.SetProvidersVolume("mb-platform-gateway-providers")
	got := s.RenderConfig(&models.Server{Name: "manager", IsLocal: true})

	for _, want := range []string{
		"version: 2",
		"file:",
		"directory: /etc/goma/providers",
		"watch: true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("manager config missing %q\n---\n%s", want, got)
		}
	}
	// The manager reads routes from the shared volume, not the HTTP provider.
	if strings.Contains(got, "http:") || strings.Contains(got, "/api/v1/provider/") {
		t.Errorf("manager config should not use the HTTP provider:\n%s", got)
	}
}

func TestRenderConfigRedis(t *testing.T) {
	// Remote edge node: a per-node Redis container, password from env.
	remote := NewService(nil, "https://miabi.example.com", "img", "miabi", "ops@example.com")
	got := remote.RenderConfig(&models.Server{Name: "edge-1"})
	for _, want := range []string{"redis:", "addr: " + RedisContainer + ":6379", "password: ${GATEWAY_REDIS_PASSWORD}"} {
		if !strings.Contains(got, want) {
			t.Errorf("remote edge config missing %q\n---\n%s", want, got)
		}
	}

	// Manager: reuses the platform Redis when configured.
	mgr := NewService(nil, "", "img", "miabi", "ops@example.com")
	mgr.SetRedis("miabi-redis:6379", "secret")
	got = mgr.RenderConfig(&models.Server{Name: "manager", IsLocal: true})
	if !strings.Contains(got, "addr: miabi-redis:6379") {
		t.Errorf("manager config should reuse the platform Redis addr\n---\n%s", got)
	}
	// The Redis password is never inlined — only the env placeholder.
	if strings.Contains(got, "secret") {
		t.Errorf("manager config leaked the redis password:\n%s", got)
	}

	// Manager with no platform Redis configured: no redis block.
	none := NewService(nil, "", "img", "miabi", "ops@example.com")
	if got := none.RenderConfig(&models.Server{Name: "manager", IsLocal: true}); strings.Contains(got, "redis:") {
		t.Errorf("manager with no Redis should omit the redis block:\n%s", got)
	}
}

func TestGatewayEnvConfigEncryptionKey(t *testing.T) {
	srv := &models.Server{Name: "edge-1"}

	// No key configured: the env var is absent.
	s := NewService(nil, "https://miabi.example.com", "img", "miabi", "ops@example.com")
	for _, e := range s.gatewayEnv(srv, "tok", "") {
		if strings.HasPrefix(e, "GOMA_CONFIG_ENCRYPTION_KEY=") {
			t.Fatalf("encryption key env present without a configured key: %q", e)
		}
	}

	// Key configured: injected verbatim into the gateway env.
	s.SetConfigEncryptionKey("  s3cr3t-key  ") // trimmed
	found := false
	for _, e := range s.gatewayEnv(srv, "tok", "") {
		if e == "GOMA_CONFIG_ENCRYPTION_KEY=s3cr3t-key" {
			found = true
		}
	}
	if !found {
		t.Errorf("gateway env missing GOMA_CONFIG_ENCRYPTION_KEY=s3cr3t-key\n%v", s.gatewayEnv(srv, "tok", ""))
	}
}

func TestValidate(t *testing.T) {
	if err := Validate("version: 2\ngateway: {}\n"); err != nil {
		t.Errorf("valid YAML rejected: %v", err)
	}
	if err := Validate(""); err == nil {
		t.Error("empty config should be invalid")
	}
	if err := Validate("key: : : bad"); err == nil {
		t.Error("malformed YAML should be invalid")
	}
}

func TestEnsureRequiresControlURL(t *testing.T) {
	s := NewService(nil, "", "img", "net", "e@x.com")
	// No custom config + no control URL → error before any Docker call.
	if err := s.Ensure(context.TODO(), nil, &models.Server{}, "tok", ""); err == nil {
		t.Fatal("expected error when control URL is unset")
	}
}

func TestConfigFilePath(t *testing.T) {
	cases := []struct {
		name       string
		entrypoint []string
		cmd        []string
		env        []string
		want       string
	}{
		{"default", nil, nil, nil, DefaultConfigFile},
		{"flag in cmd", []string{"/goma"}, []string{"server", "--config", "/cfg/goma.yml"}, nil, "/cfg/goma.yml"},
		{"short flag", nil, []string{"server", "-c", "/custom.yml"}, nil, "/custom.yml"},
		{"flag equals", nil, []string{"server", "--config=/eq.yml"}, nil, "/eq.yml"},
		{"flag in entrypoint", []string{"/goma", "-c", "/ep.yml"}, []string{"server"}, nil, "/ep.yml"},
		{"env var", nil, []string{"server"}, []string{"GOMA_CONFIG_FILE=/env.yml"}, "/env.yml"},
		{"flag beats env", nil, []string{"--config", "/flag.yml"}, []string{"GOMA_CONFIG_FILE=/env.yml"}, "/flag.yml"},
		{"empty env ignored", nil, nil, []string{"GOMA_CONFIG_FILE="}, DefaultConfigFile},
	}
	for _, c := range cases {
		if got := ConfigFilePath(c.entrypoint, c.cmd, c.env); got != c.want {
			t.Errorf("%s: ConfigFilePath = %q, want %q", c.name, got, c.want)
		}
	}
}
