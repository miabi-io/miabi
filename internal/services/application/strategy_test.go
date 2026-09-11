// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestResolveStrategy(t *testing.T) {
	s := &Service{}
	rel := uint(1)
	deployed := &models.Application{DeployStrategy: models.DeployCanary, CurrentReleaseID: &rel}
	rolling := &models.Application{DeployStrategy: models.DeployRolling, CurrentReleaseID: &rel}
	fresh := &models.Application{DeployStrategy: models.DeployCanary} // never deployed

	cases := []struct {
		name      string
		app       *models.Application
		requested models.DeployStrategy
		want      models.DeployStrategy
	}{
		{"explicit recreate overrides default", rolling, models.DeployRecreate, models.DeployRecreate},
		{"empty uses app default", deployed, "", models.DeployCanary},
		{"invalid uses app default", deployed, "bogus", models.DeployCanary},
		{"canary on fresh app falls back to rolling", fresh, models.DeployCanary, models.DeployRolling},
		{"empty + invalid app default => rolling", &models.Application{}, "", models.DeployRolling},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := s.resolveStrategy(c.app, c.requested); got != c.want {
				t.Errorf("resolveStrategy = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCheckResourceCaps(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)
	const core = int64(1_000_000_000)

	// Unlimited (0 caps) always passes.
	if err := checkResourceCaps(0, 0, 8*gb, 4*core); err != nil {
		t.Errorf("unlimited caps should pass, got %v", err)
	}
	// Within caps passes.
	if err := checkResourceCaps(2048, 4, gb, 2*core); err != nil {
		t.Errorf("within caps should pass, got %v", err)
	}
	// Over memory cap rejects.
	if err := checkResourceCaps(512, 0, gb, 0); !errors.Is(err, ErrResourceCap) {
		t.Errorf("over memory cap should return ErrResourceCap, got %v", err)
	}
	// Over CPU cap rejects.
	if err := checkResourceCaps(0, 1, 0, 2*core); !errors.Is(err, ErrResourceCap) {
		t.Errorf("over cpu cap should return ErrResourceCap, got %v", err)
	}
	// Exactly at the cap passes.
	if err := checkResourceCaps(1024, 2, gb, 2*core); err != nil {
		t.Errorf("at-cap should pass, got %v", err)
	}
}

func TestNormalizeHealthcheck(t *testing.T) {
	app := &models.Application{HealthcheckType: "bogus", HealthcheckIntervalSeconds: 0, HealthcheckTimeoutSeconds: 0, HealthcheckRetries: 0}
	normalizeHealthcheck(app)
	if app.HealthcheckType != models.HealthcheckNone {
		t.Errorf("invalid type should normalize to none, got %q", app.HealthcheckType)
	}
	if app.HealthcheckIntervalSeconds != 30 || app.HealthcheckTimeoutSeconds != 5 || app.HealthcheckRetries != 3 {
		t.Errorf("timing defaults not applied: %+v", app)
	}
}

func TestNormalizeDeployConfig(t *testing.T) {
	app := &models.Application{DeployStrategy: "nonsense", CanaryInitialWeight: 0, CanaryStepWeight: 200, CanaryStepIntervalSeconds: 3}
	normalizeDeployConfig(app)
	if app.DeployStrategy != models.DeployRolling {
		t.Errorf("strategy = %q, want rolling", app.DeployStrategy)
	}
	if app.CanaryInitialWeight != 1 {
		t.Errorf("initial weight = %d, want clamped to 1", app.CanaryInitialWeight)
	}
	if app.CanaryStepWeight != 99 {
		t.Errorf("step weight = %d, want clamped to 99", app.CanaryStepWeight)
	}
	if app.CanaryStepIntervalSeconds != 10 {
		t.Errorf("interval = %d, want clamped to 10", app.CanaryStepIntervalSeconds)
	}
}
