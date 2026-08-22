// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// liveApp is an app with a canary running at the given weight, so validation is
// judged against a real rollout rather than a projected one.
func liveApp(weight int) *models.Application {
	relID := uint(9)
	return &models.Application{ID: 1, CanaryReleaseID: &relID, CanaryWeight: weight, CanaryInitialWeight: 10}
}

func rule(source, name, op, value string) models.CanaryMatchRule {
	return models.CanaryMatchRule{Source: source, Name: name, Operator: op, Value: value}
}

// check runs the same normalize-then-validate path SetCanaryRouting takes, so a
// payload is judged exactly as the API judges it.
func check(app *models.Application, in CanaryRouting) error {
	norm, err := normalizeCanaryRouting(in)
	if err != nil {
		return err
	}
	return validateCanaryRouting(app, norm)
}

func manual(exclusive bool, rules ...models.CanaryMatchRule) CanaryRouting {
	return CanaryRouting{Mode: models.CanaryModeManual, Exclusive: exclusive, Match: rules}
}

// Each case is a config that reads as correct and routes somewhere surprising.
func TestCanaryRoutingRefusals(t *testing.T) {
	cases := []struct {
		name string
		app  *models.Application
		in   CanaryRouting
		want error
	}{
		{
			// Would hand the canary 100% of production.
			"exclusive with no match rules",
			liveApp(20), manual(true),
			ErrCanaryExclusiveNoMatch,
		},
		{
			// Rules that look active while routing nothing: matching requests join
			// a pool where the canary has no share.
			"pooled rules at zero weight",
			liveApp(0), manual(false, rule("header", "X-Canary", "equals", "true")),
			ErrCanaryPooledZeroWeight,
		},
		{
			"rules in automatic mode",
			liveApp(20),
			CanaryRouting{Mode: models.CanaryModeAuto, Match: []models.CanaryMatchRule{rule("header", "X-Canary", "equals", "true")}},
			ErrCanaryRulesNeedManual,
		},
		{
			"unknown source",
			liveApp(20), manual(false, rule("body", "user", "equals", "x")),
			ErrCanaryMatchSource,
		},
		{
			"unknown operator",
			liveApp(20), manual(false, rule("header", "X-Canary", "matches", "x")),
			ErrCanaryMatchOperator,
		},
		{
			"header rule with no name",
			liveApp(20), manual(false, rule("header", "", "equals", "x")),
			ErrCanaryMatchName,
		},
		{
			"rule with no value",
			liveApp(20), manual(false, rule("header", "X-Canary", "equals", "")),
			ErrCanaryMatchValue,
		},
		{
			"priority out of range",
			liveApp(20),
			CanaryRouting{Mode: models.CanaryModeManual, Priority: 5000, Exclusive: true, Match: []models.CanaryMatchRule{rule("header", "X-Canary", "equals", "true")}},
			ErrCanaryPriority,
		},
		{
			"unknown mode",
			liveApp(20), CanaryRouting{Mode: "gradual"},
			ErrCanaryModeInvalid,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := check(c.app, c.in)
			if !errors.Is(err, c.want) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestCanaryRoutingTooManyRules(t *testing.T) {
	in := manual(false)
	for i := 0; i <= maxCanaryMatchRules; i++ {
		in.Match = append(in.Match, rule("header", "X-Canary", "equals", "true"))
	}
	if err := check(liveApp(20), in); !errors.Is(err, ErrCanaryTooManyRules) {
		t.Errorf("got %v, want ErrCanaryTooManyRules", err)
	}
}

// A pattern that does not compile fails closed at the gateway — invisibly. It is
// refused at save, naming the pattern.
func TestCanaryRoutingRejectsUncompilableRegex(t *testing.T) {
	err := check(liveApp(20), manual(false, rule("header", "X-Canary", "regex", "beta-[")))
	var re *ErrCanaryMatchRegex
	if !errors.As(err, &re) {
		t.Fatalf("got %v, want ErrCanaryMatchRegex", err)
	}
	if re.Pattern != "beta-[" {
		t.Errorf("error does not name the offending pattern: %v", err)
	}
}

func TestCanaryRoutingAcceptsValidRules(t *testing.T) {
	valid := []CanaryRouting{
		manual(true, rule("header", "X-Canary-User", "equals", "true")),
		manual(false, rule("cookie", "beta_user", "in", "admin,tester")),
		manual(false, rule("query", "version", "starts_with", "beta")),
		manual(true, rule("ip", "", "equals", "203.0.113.7")), // an ip rule needs no name
		manual(false, rule("header", "X-Build", "regex", "^v2\\.[0-9]+$")),
		{Mode: models.CanaryModeManual}, // manual weight, no rules
		{Mode: models.CanaryModeAuto},   // the automatic ramp
	}
	for _, in := range valid {
		if err := check(liveApp(20), in); err != nil {
			t.Errorf("rejected a valid config %+v: %v", in, err)
		}
	}
}

// A rule set saved before any canary exists is judged against the weight the
// next rollout will actually start at, not against 0.
func TestCanaryRoutingValidatesAgainstProjectedWeight(t *testing.T) {
	idle := &models.Application{ID: 1, CanaryInitialWeight: 10}
	if err := check(idle, manual(false, rule("header", "X-Canary", "equals", "true"))); err != nil {
		t.Errorf("rules saved ahead of a canary were rejected: %v", err)
	}
}

// Automatic mode is the same model with the rule set empty: exclusive and
// priority are dropped rather than silently retained.
func TestNormalizeCanaryRoutingClearsAdvancedFieldsInAutoMode(t *testing.T) {
	out, err := normalizeCanaryRouting(CanaryRouting{Mode: models.CanaryModeAuto, Exclusive: true, Priority: 9})
	if err != nil {
		t.Fatal(err)
	}
	if out.Exclusive || out.Priority != 0 {
		t.Errorf("auto mode kept advanced fields: %+v", out)
	}
}

func TestNormalizeCanaryRoutingTrimsAndLowercases(t *testing.T) {
	out, err := normalizeCanaryRouting(manual(false, rule("  HEADER ", " X-Canary ", " Equals ", " true ")))
	if err != nil {
		t.Fatal(err)
	}
	got := out.Match[0]
	if got.Source != "header" || got.Operator != "equals" || got.Name != "X-Canary" || got.Value != "true" {
		t.Errorf("normalized rule = %+v", got)
	}
}

// An ip rule is only as trustworthy as the gateway's trusted-proxy config, so
// saving one must say so and point at the documentation.
func TestCanaryRoutingWarnsOnIPRules(t *testing.T) {
	w := canaryRoutingWarnings(CanaryRouting{Mode: models.CanaryModeManual, Exclusive: true, Match: []models.CanaryMatchRule{rule("ip", "", "equals", "203.0.113.7")}})
	if len(w) != 1 {
		t.Fatalf("expected one warning, got %v", w)
	}
	if !strings.Contains(w[0], "trustedProxies") || !strings.Contains(w[0], GomaProxyDocsURL) {
		t.Errorf("warning does not explain or link the risk: %q", w[0])
	}
	if len(canaryRoutingWarnings(manual(false, rule("header", "X-Canary", "equals", "true")))) != 0 {
		t.Errorf("header rules must not warn about client IP")
	}
}
