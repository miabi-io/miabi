// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package route

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func canaryApp(weight int, exclusive bool, rules ...models.CanaryMatchRule) *models.Application {
	relID := uint(9)
	return &models.Application{
		ID: 5, CanaryReleaseID: &relID, CanaryWeight: weight,
		CanaryMode: models.CanaryModeManual, CanaryExclusive: exclusive, CanaryMatch: rules,
	}
}

var headerRule = models.CanaryMatchRule{Source: "header", Name: "X-Canary", Operator: "equals", Value: "true"}

func TestAliasBackendsAttachesMatchRules(t *testing.T) {
	b := aliasBackends(canaryApp(20, false, headerRule), 80, "http")
	if len(b) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(b))
	}
	if len(b[0].Match) != 0 || b[0].Exclusive {
		t.Errorf("rules leaked onto the stable backend: %+v", b[0])
	}
	if len(b[1].Match) != 1 || b[1].Match[0].Name != "X-Canary" || b[1].Match[0].Operator != "equals" {
		t.Errorf("canary backend missing its match rule: %+v", b[1])
	}
	if b[1].Exclusive {
		t.Errorf("non-exclusive canary rendered as exclusive: %+v", b[1])
	}
	if b[0].Weight != 80 || b[1].Weight != 20 {
		t.Errorf("weights = %d/%d, want 80/20", b[0].Weight, b[1].Weight)
	}
}

func TestAliasBackendsExclusiveCarriesPriority(t *testing.T) {
	app := canaryApp(10, true, headerRule)
	app.CanaryPriority = 7
	b := aliasBackends(app, 80, "http")
	if !b[1].Exclusive || b[1].Priority != 7 {
		t.Errorf("exclusive/priority not carried: %+v", b[1])
	}
}

// An exclusive canary serves the traffic its rules select whatever its weight,
// so it must stay in the route at 0 rather than disappearing.
func TestAliasBackendsExclusiveSurvivesZeroWeight(t *testing.T) {
	b := aliasBackends(canaryApp(0, true, headerRule), 80, "http")
	if len(b) != 2 {
		t.Fatalf("exclusive canary dropped at weight 0: %+v", b)
	}
	if b[0].Weight != 100 || b[1].Weight != 0 {
		t.Errorf("weights = %d/%d, want 100/0", b[0].Weight, b[1].Weight)
	}
	if !b[1].Exclusive {
		t.Errorf("canary lost exclusive: %+v", b[1])
	}
}

// Exclusive without rules would hand the canary every request. Validation
// rejects it at save; the renderer refuses to emit it as a second line of
// defence for a row that predates the check or was written out of band.
func TestAliasBackendsExclusiveWithoutRulesIsNotExclusive(t *testing.T) {
	b := aliasBackends(canaryApp(20, true), 80, "http")
	if len(b) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(b))
	}
	if b[1].Exclusive {
		t.Errorf("exclusive rendered without match rules — would take all traffic: %+v", b[1])
	}
}

// A canary with no rules must render exactly as it always has.
func TestAliasBackendsPlainCanaryUnchanged(t *testing.T) {
	b := aliasBackends(canaryApp(20, false), 80, "http")
	if len(b) != 2 || b[1].Exclusive || b[1].Priority != 0 || b[1].Match != nil {
		t.Errorf("plain canary picked up advanced fields: %+v", b[1])
	}
}
