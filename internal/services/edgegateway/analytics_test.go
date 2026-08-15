// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func analyticsService(t *testing.T, stream, platformRedis string) *Service {
	t.Helper()
	s := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	s.SetRedis(platformRedis, "pw")
	s.SetAnalytics(stream)
	return s
}

// The manager's gateway publishes to the platform Redis — the one the analytics
// consumer reads — so its rendered config turns analytics on.
func TestManagerGatewayRendersAnalytics(t *testing.T) {
	s := analyticsService(t, "goma:analytics", "miabi-redis:6379")
	got := s.RenderConfig(&models.Server{Name: "manager", IsLocal: true})

	for _, want := range []string{
		"analytics:",
		"stream: goma:analytics",
		"sample: 1",
		"maxLen: 1000000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, got)
		}
	}
}

// A remote edge node publishes to its own Redis, which the agent drains and
// forwards to the manager — so it publishes too, into that local buffer.
func TestRemoteEdgeNodeRendersAnalytics(t *testing.T) {
	s := analyticsService(t, "goma:analytics", "miabi-redis:6379")
	got := s.RenderConfig(&models.Server{Name: "edge-1"})

	for _, want := range []string{"analytics:", "stream: goma:analytics", RedisContainer + ":6379"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, got)
		}
	}
}

// Every event names the gateway that served it, or edge traffic is
// indistinguishable from the manager's once both land in one stream.
func TestEdgeGatewayEnvCarriesGatewayID(t *testing.T) {
	s := analyticsService(t, "goma:analytics", "miabi-redis:6379")
	env := s.gatewayEnv(&models.Server{Name: "edge-1"}, "tok", "redispw")

	var found bool
	for _, e := range env {
		if e == gatewayIDEnv+"=edge-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("gateway env missing %s=edge-1: %v", gatewayIDEnv, env)
	}
}

// With no stream wired there is nothing to name a gateway for.
func TestNoGatewayIDWithoutAnalytics(t *testing.T) {
	s := analyticsService(t, "", "miabi-redis:6379")
	for _, e := range s.gatewayEnv(&models.Server{Name: "edge-1"}, "tok", "redispw") {
		if strings.HasPrefix(e, gatewayIDEnv+"=") {
			t.Errorf("gateway id set with analytics off: %v", e)
		}
	}
}

// With the platform consumer switched off (MIABI_ANALYTICS_ENABLED=false) no
// stream is wired, so nothing is published anywhere.
func TestNoAnalyticsWithoutAConsumer(t *testing.T) {
	s := analyticsService(t, "", "miabi-redis:6379")
	if got := s.RenderConfig(&models.Server{Name: "manager", IsLocal: true}); strings.Contains(got, "analytics:") {
		t.Errorf("analytics must stay off when no consumer reads the stream\n---\n%s", got)
	}
}

// No platform Redis means the manager's gateway has nowhere to publish to.
func TestNoAnalyticsWithoutPlatformRedis(t *testing.T) {
	s := analyticsService(t, "goma:analytics", "")
	if got := s.RenderConfig(&models.Server{Name: "manager", IsLocal: true}); strings.Contains(got, "analytics:") {
		t.Errorf("analytics needs a Redis to publish to\n---\n%s", got)
	}
}
