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

// A remote edge node runs its own per-node Redis for cache and rate limiting.
// Events written there are read by nobody, so enabling analytics would fill that
// node's Redis to maxLen and never reach a dashboard.
func TestRemoteEdgeNodeOmitsAnalytics(t *testing.T) {
	s := analyticsService(t, "goma:analytics", "miabi-redis:6379")
	got := s.RenderConfig(&models.Server{Name: "edge-1"})

	if strings.Contains(got, "analytics:") {
		t.Errorf("a remote edge node must not publish analytics nobody consumes\n---\n%s", got)
	}
	// It still gets its per-node Redis for the features that do work there.
	if !strings.Contains(got, RedisContainer+":6379") {
		t.Errorf("remote node lost its per-node Redis\n---\n%s", got)
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

// No platform Redis means nowhere to publish to at all.
func TestNoAnalyticsWithoutPlatformRedis(t *testing.T) {
	s := analyticsService(t, "goma:analytics", "")
	if got := s.RenderConfig(&models.Server{Name: "manager", IsLocal: true}); strings.Contains(got, "analytics:") {
		t.Errorf("analytics needs a Redis to publish to\n---\n%s", got)
	}
}
