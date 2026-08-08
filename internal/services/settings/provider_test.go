// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package settings

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// newCached builds a Provider with a pre-populated cache (no DB) for testing the
// typed getters in isolation.
func newCached(values map[string]models.Setting) *Provider {
	return &Provider{cache: values}
}

func TestProviderBool(t *testing.T) {
	p := newCached(map[string]models.Setting{
		"on":  {Key: "on", Value: "true", Type: models.SettingTypeBool},
		"off": {Key: "off", Value: "false", Type: models.SettingTypeBool},
		"bad": {Key: "bad", Value: "nope", Type: models.SettingTypeBool},
	})
	if !p.Bool("on", false) {
		t.Errorf("expected true for 'on'")
	}
	if p.Bool("off", true) {
		t.Errorf("expected false for 'off'")
	}
	if !p.Bool("bad", true) {
		t.Errorf("expected default for unparseable value")
	}
	if !p.Bool("missing", true) {
		t.Errorf("expected default for missing key")
	}
}

func TestProviderInt(t *testing.T) {
	p := newCached(map[string]models.Setting{
		"n": {Key: "n", Value: "42", Type: models.SettingTypeInt},
	})
	if got := p.Int("n", 0); got != 42 {
		t.Errorf("expected 42, got %d", got)
	}
	if got := p.Int("missing", 7); got != 7 {
		t.Errorf("expected default 7, got %d", got)
	}
}

// TestProviderRefreshIfStale verifies the lazy-refresh gating: a stale cache triggers a reload on the next
// read — here a no-op nil-repo reload that just re-stamps loadedAt — so loadedAt advances and the cache is
// still served. A fresh cache, with the ttl disabled, is never reloaded.
func TestProviderRefreshIfStale(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	p := &Provider{
		cache:    map[string]models.Setting{"k": {Key: "k", Value: "v", Type: models.SettingTypeString}},
		ttl:      cacheTTL,
		loadedAt: old,
	}
	if got := p.String("k", "x"); got != "v" {
		t.Fatalf("expected cached value, got %q", got)
	}
	p.mu.RLock()
	advanced := p.loadedAt.After(old)
	p.mu.RUnlock()
	if !advanced {
		t.Error("stale cache should have triggered a reload (loadedAt unchanged)")
	}

	// ttl == 0 disables auto-refresh: loadedAt must not move.
	p2 := &Provider{cache: map[string]models.Setting{"k": {Value: "v"}}, loadedAt: old}
	_ = p2.String("k", "x")
	if p2.loadedAt != old {
		t.Error("ttl=0 provider should not auto-refresh")
	}
}

func TestProviderString(t *testing.T) {
	p := newCached(map[string]models.Setting{
		"s": {Key: "s", Value: "hello", Type: models.SettingTypeString},
	})
	if got := p.String("s", "x"); got != "hello" {
		t.Errorf("expected hello, got %q", got)
	}
	if got := p.String("missing", "fallback"); got != "fallback" {
		t.Errorf("expected fallback, got %q", got)
	}
}
