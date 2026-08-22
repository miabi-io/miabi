// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
)

func TestAdvancedCanaryRequested(t *testing.T) {
	rule := models.CanaryMatchRule{Source: "header", Name: "X-Canary", Operator: "equals", Value: "true"}
	cases := []struct {
		name string
		in   application.CanaryRouting
		want bool
	}{
		{"the automatic ramp", application.CanaryRouting{Mode: models.CanaryModeAuto}, false},
		{"manual mode", application.CanaryRouting{Mode: models.CanaryModeManual}, true},
		{"match rules", application.CanaryRouting{Mode: models.CanaryModeManual, Match: []models.CanaryMatchRule{rule}}, true},
		{"exclusive", application.CanaryRouting{Mode: models.CanaryModeAuto, Exclusive: true}, true},
		{"priority", application.CanaryRouting{Mode: models.CanaryModeAuto, Priority: 3}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := advancedCanaryRequested(c.in); got != c.want {
				t.Errorf("advancedCanaryRequested = %v, want %v", got, c.want)
			}
		})
	}
}

func TestIsCanaryConfigErr(t *testing.T) {
	refusals := []error{
		application.ErrCanaryModeInvalid,
		application.ErrCanaryRulesNeedManual,
		application.ErrCanaryExclusiveNoMatch,
		application.ErrCanaryPooledZeroWeight,
		application.ErrCanaryTooManyRules,
		application.ErrCanaryMatchSource,
		application.ErrCanaryMatchOperator,
		application.ErrCanaryMatchName,
		application.ErrCanaryMatchValue,
		application.ErrCanaryPriority,
		&application.ErrCanaryMatchRegex{Pattern: "beta-[", Err: errors.New("bad")},
	}
	for _, err := range refusals {
		if !isCanaryConfigErr(err) {
			t.Errorf("%v not recognised as a configuration refusal", err)
		}
	}
	if isCanaryConfigErr(application.ErrNoCanary) {
		t.Errorf("ErrNoCanary is a conflict (409), not a bad request")
	}
	if isCanaryConfigErr(errors.New("database is down")) {
		t.Errorf("an unrelated failure must not be reported as a bad request")
	}
}
