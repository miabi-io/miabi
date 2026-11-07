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

package edgegateway

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

const httpProviderConfig = `
version: 2
gateway:
  providers:
    http:
      enabled: true
      endpoint: https://miabi.example.com/api/v1/provider/manager
`

// The Redis choice follows the node, not the route provider.
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

	// The agent drains the node's own stream and forwards it to the manager.
	edge := &models.Server{Name: "edge-1"}
	if got := s.redisAddrFor(edge); got != RedisContainer+":6379" {
		t.Errorf("edge redis = %q, want the per-node Redis", got)
	}
	if !s.analyticsEnabledFor(edge) {
		t.Error("an edge node buffers analytics on its own Redis for the agent to forward")
	}
}

// Without a Miabi-owned volume the gateway would watch a directory nothing writes to, so it polls.
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

func TestEffectiveFileProviderFollowsTheStoredConfig(t *testing.T) {
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
		// Configs are validated on save, so this covers only hand edits outside the API.
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

// The manager has no address and runs the compose gateway, so reloads must target its container name.
func TestReloadHostForTheManager(t *testing.T) {
	s := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")

	host, err := s.reloadHost(&models.Server{IsLocal: true})
	if err != nil || host != CentralContainerName {
		t.Errorf("manager reload host = (%q, %v), want %q", host, err, CentralContainerName)
	}
	if host == ContainerName {
		t.Error("the manager was addressed as the per-node gateway, which does not run there")
	}
	if host, err = s.reloadHost(&models.Server{Name: "edge-1", Address: "10.0.0.5"}); err != nil || host != "10.0.0.5" {
		t.Errorf("edge reload host = (%q, %v), want its address", host, err)
	}
	if _, err = s.reloadHost(&models.Server{Name: "edge-1"}); err == nil {
		t.Error("an edge node with no address cannot be reloaded — that must be an error")
	}
}
