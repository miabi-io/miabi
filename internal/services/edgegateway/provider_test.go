// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// httpProviderConfig is a manager gateway switched to poll the control plane
// instead of watching the providers volume.
const httpProviderConfig = `
version: 2
gateway:
  providers:
    http:
      enabled: true
      endpoint: https://miabi.example.com/api/v1/provider/manager
`

// Where a gateway runs and how it learns its routes are separate questions. The
// manager shares the control plane's Redis whichever provider it uses — the
// Redis choice follows the node, not the config.
func TestManagerSharesPlatformRedisOnEitherProvider(t *testing.T) {
	s := analyticsService(t, "goma:analytics", "miabi-redis:6379")

	for _, srv := range []*models.Server{
		{Name: "manager", IsLocal: true},
		{Name: "manager", IsLocal: true, GatewayConfigYAML: httpProviderConfig},
	} {
		if got := s.redisAddrFor(srv); got != "miabi-redis:6379" {
			t.Errorf("manager redis = %q, want the platform Redis", got)
		}
		if !s.analyticsEnabledFor(srv) {
			t.Error("a manager gateway publishes analytics whichever provider it uses")
		}
	}

	// A remote edge node is self-contained: its own Redis, and no analytics,
	// because nothing reads that Redis.
	edge := &models.Server{Name: "edge-1"}
	if got := s.redisAddrFor(edge); got != RedisContainer+":6379" {
		t.Errorf("edge redis = %q, want the per-node Redis", got)
	}
	if s.analyticsEnabledFor(edge) {
		t.Error("a remote edge node must not publish analytics nobody consumes")
	}
}

// The manager can only watch route files if Miabi can hand it the volume it writes them to. A stack
// install owns that volume; a manual install's mounts were made by the operator, so Miabi cannot name
// one — and a gateway watching a directory nothing writes to would serve no routes. It polls instead.
func TestManagerFallsBackToHTTPWithoutAKnownVolume(t *testing.T) {
	manager := &models.Server{Name: "manager", IsLocal: true}

	// Manual install: no providers volume known.
	manual := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	got := manual.RenderConfig(manager)
	if !strings.Contains(got, "http:") || !strings.Contains(got, "/api/v1/provider/manager") {
		t.Errorf("a manual-install manager must poll the HTTP provider\n---\n%s", got)
	}
	if strings.Contains(got, "directory: /etc/goma/providers") {
		t.Errorf("a manual-install manager must not watch a directory it was never given\n---\n%s", got)
	}
	// …and it needs the reload plumbing a poller depends on.
	if !strings.Contains(got, "reload:") {
		t.Errorf("a polling manager needs the reload endpoint\n---\n%s", got)
	}
	if manual.usesFileProvider(manager) {
		t.Error("usesFileProvider must be false without a known volume")
	}

	// Stack install: the volume is known, so it watches it.
	stack := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	stack.SetProvidersVolume("mb-platform-gateway-providers")
	if got = stack.RenderConfig(manager); !strings.Contains(got, "directory: /etc/goma/providers") {
		t.Errorf("a stack-install manager must watch the route directory\n---\n%s", got)
	}
	if !stack.usesFileProvider(manager) {
		t.Error("usesFileProvider must be true once the volume is known")
	}
}

// The runtime plumbing that depends on the provider follows the gateway's actual
// config, so a manager switched to HTTP polling is treated like any poller.
func TestEffectiveFileProviderFollowsTheStoredConfig(t *testing.T) {
	// A stack install: Miabi knows the volume backing its route directory, so the
	// manager's default is the file provider.
	s := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	s.SetProvidersVolume("mb-platform-gateway-providers")

	cases := []struct {
		name string
		srv  *models.Server
		want bool
	}{
		{"manager default watches the volume", &models.Server{IsLocal: true}, true},
		{"manager switched to http polls", &models.Server{IsLocal: true, GatewayConfigYAML: httpProviderConfig}, false},
		{"edge node polls", &models.Server{}, false},
		{
			"edge node given a file provider watches",
			&models.Server{GatewayConfigYAML: "version: 2\ngateway:\n  providers:\n    file:\n      enabled: true\n"},
			true,
		},
		// The config is validated on save, so this only covers one edited by hand
		// outside the API: fall back to the node's default rather than guessing.
		{"unparseable config falls back", &models.Server{IsLocal: true, GatewayConfigYAML: "\tnot: [yaml"}, true},
		{"config with no provider block falls back", &models.Server{GatewayConfigYAML: "version: 2\n"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.effectiveFileProvider(tc.srv); got != tc.want {
				t.Errorf("effectiveFileProvider = %v, want %v", got, tc.want)
			}
		})
	}
}

// A polling gateway needs the reload token to accept the on-demand nudge —
// including a manager that polls, which previously never got one.
func TestReloadTokenFollowsTheProvider(t *testing.T) {
	s := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	s.SetProvidersVolume("mb-platform-gateway-providers") // stack install

	has := func(srv *models.Server) bool {
		for _, e := range s.gatewayEnv(srv, "tok", "") {
			if strings.HasPrefix(e, reloadTokenEnv+"=") {
				return true
			}
		}
		return false
	}
	if has(&models.Server{IsLocal: true}) {
		t.Error("a manager watching the volume needs no reload token")
	}
	if !has(&models.Server{IsLocal: true, GatewayConfigYAML: httpProviderConfig}) {
		t.Error("a manager that polls needs a reload token")
	}
	if !has(&models.Server{Name: "edge-1"}) {
		t.Error("a remote edge node needs a reload token")
	}
}

// The manager's server record carries no address — it is "here" — so a reload
// must reach its gateway over the shared proxy network by container name rather
// than failing for want of an address.
func TestReloadHostForTheManager(t *testing.T) {
	s := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")

	host, err := s.reloadHost(&models.Server{IsLocal: true})
	if err != nil || host != ContainerName {
		t.Errorf("manager reload host = (%q, %v), want the gateway container name", host, err)
	}
	if host, err = s.reloadHost(&models.Server{Name: "edge-1", Address: "10.0.0.5"}); err != nil || host != "10.0.0.5" {
		t.Errorf("edge reload host = (%q, %v), want its address", host, err)
	}
	if _, err = s.reloadHost(&models.Server{Name: "edge-1"}); err == nil {
		t.Error("an edge node with no address cannot be reloaded — that must be an error")
	}
}
