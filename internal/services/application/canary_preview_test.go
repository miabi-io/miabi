// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func previewApp(weight int, exclusive bool, rules ...models.CanaryMatchRule) *models.Application {
	relID := uint(9)
	return &models.Application{
		ID: 1, CanaryReleaseID: &relID, CanaryWeight: weight, CanaryInitialWeight: 10,
		CanaryMode: models.CanaryModeManual, CanaryExclusive: exclusive, CanaryMatch: rules,
	}
}

func TestPreviewExclusiveMatchGoesEntirelyToCanary(t *testing.T) {
	app := previewApp(10, true, rule("header", "X-Canary-User", "equals", "true"))
	got := PreviewCanaryRouting(app, CanaryPreviewRequest{Headers: map[string]string{"X-Canary-User": "true"}})
	if got.Backend != CanaryPreviewCanary || got.CanaryChance != 100 {
		t.Errorf("got %s at %d%%, want canary at 100%%: %s", got.Backend, got.CanaryChance, got.Reason)
	}
}

func TestPreviewNonMatchingRequestGoesToStable(t *testing.T) {
	app := previewApp(10, true, rule("header", "X-Canary-User", "equals", "true"))
	got := PreviewCanaryRouting(app, CanaryPreviewRequest{Headers: map[string]string{"X-Canary-User": "false"}})
	if got.Backend != CanaryPreviewStable || got.CanaryChance != 0 {
		t.Errorf("got %s at %d%%, want stable", got.Backend, got.CanaryChance)
	}
	if got.Matched || len(got.Rules) != 1 || got.Rules[0].Actual != "false" {
		t.Errorf("preview does not show why it did not match: %+v", got.Rules)
	}
}

// Every rule must hold (AND), as at the gateway.
func TestPreviewRequiresEveryRule(t *testing.T) {
	app := previewApp(10, true,
		rule("header", "X-Canary-User", "equals", "true"),
		rule("query", "version", "equals", "beta"),
	)
	got := PreviewCanaryRouting(app, CanaryPreviewRequest{Headers: map[string]string{"X-Canary-User": "true"}})
	if got.Backend != CanaryPreviewStable {
		t.Errorf("one rule out of two was enough: %s", got.Reason)
	}
	if !got.Rules[0].Matched || got.Rules[1].Matched {
		t.Errorf("per-rule verdicts wrong: %+v", got.Rules)
	}
}

func TestPreviewNonExclusiveMatchIsAWeightedSplit(t *testing.T) {
	app := previewApp(25, false, rule("cookie", "beta_user", "in", "admin,tester"))
	got := PreviewCanaryRouting(app, CanaryPreviewRequest{Cookies: map[string]string{"beta_user": "tester"}})
	if got.Backend != CanaryPreviewSplit || got.CanaryChance != 25 {
		t.Errorf("got %s at %d%%, want split at 25%%", got.Backend, got.CanaryChance)
	}
}

func TestPreviewPlainCanaryIsASplitForEveryRequest(t *testing.T) {
	got := PreviewCanaryRouting(previewApp(30, false), CanaryPreviewRequest{})
	if got.Backend != CanaryPreviewSplit || got.CanaryChance != 30 {
		t.Errorf("got %s at %d%%, want split at 30%%", got.Backend, got.CanaryChance)
	}
}

// Header names are case-insensitive, as HTTP makes them.
func TestPreviewHeaderLookupIsCaseInsensitive(t *testing.T) {
	app := previewApp(10, true, rule("header", "x-canary-user", "equals", "true"))
	got := PreviewCanaryRouting(app, CanaryPreviewRequest{Headers: map[string]string{"X-Canary-User": "true"}})
	if got.Backend != CanaryPreviewCanary {
		t.Errorf("header lookup missed a differently-cased name: %+v", got.Rules)
	}
}

// The preview's comparisons must be the gateway's, operator for operator.
func TestPreviewOperators(t *testing.T) {
	cases := []struct {
		op, actual, expected string
		want                 bool
	}{
		{models.CanaryMatchOpEquals, "beta", "beta", true},
		{models.CanaryMatchOpEquals, "beta", "alpha", false},
		{models.CanaryMatchOpNotEquals, "beta", "alpha", true},
		{models.CanaryMatchOpContains, "v2-beta-3", "beta", true},
		{models.CanaryMatchOpNotContains, "v2-beta-3", "alpha", true},
		{models.CanaryMatchOpStartsWith, "beta-3", "beta", true},
		{models.CanaryMatchOpEndsWith, "v2-beta", "beta", true},
		{models.CanaryMatchOpRegex, "v2.11", `^v2\.[0-9]+$`, true},
		{models.CanaryMatchOpRegex, "v3.11", `^v2\.[0-9]+$`, false},
		{models.CanaryMatchOpIn, "tester", "admin, tester ,dev", true}, // list entries are trimmed
		{models.CanaryMatchOpIn, "guest", "admin,tester", false},
		{"unknown_operator", "x", "x", false}, // never matches, as at the gateway
	}
	for _, c := range cases {
		if got := evaluateCanaryOperator(c.op, c.actual, c.expected); got != c.want {
			t.Errorf("%s(%q, %q) = %v, want %v", c.op, c.actual, c.expected, got, c.want)
		}
	}
}

// With no canary running, the preview projects onto the weight the next rollout
// will start at — the whole point is to trust a rule set before deploying it.
func TestPreviewProjectsWithoutARunningCanary(t *testing.T) {
	idle := &models.Application{
		ID: 1, CanaryInitialWeight: 15, CanaryMode: models.CanaryModeManual,
		CanaryMatch: []models.CanaryMatchRule{rule("query", "version", "equals", "beta")},
	}
	got := PreviewCanaryRouting(idle, CanaryPreviewRequest{Query: map[string]string{"version": "beta"}})
	if got.Backend != CanaryPreviewSplit || got.CanaryChance != 15 {
		t.Errorf("got %s at %d%%, want split at the projected 15%%", got.Backend, got.CanaryChance)
	}
}
