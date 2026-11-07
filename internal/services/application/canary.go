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

package application

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// The gateway ANDs every rule, so long sets are almost always a mistake.
const maxCanaryMatchRules = 10

// Each misconfiguration gets its own refusal because each looks right but routes surprisingly.
var (
	ErrCanaryModeInvalid = errors.New("canary mode must be auto or manual")
	// The ramp owns the weight in automatic mode, which is defined as the empty rule set.
	ErrCanaryRulesNeedManual = errors.New("canary match rules require manual mode")
	// Without rules the gateway would hand the canary every request.
	ErrCanaryExclusiveNoMatch = errors.New("an exclusive canary needs at least one match rule; without one it takes all traffic")
	// At 0% a pooled canary gets no share, so the rules look active while doing nothing.
	ErrCanaryPooledZeroWeight = errors.New("a canary at 0% needs exclusive routing; otherwise its match rules send no traffic")
	ErrCanaryTooManyRules     = fmt.Errorf("a canary may have at most %d match rules", maxCanaryMatchRules)
	ErrCanaryMatchSource      = fmt.Errorf("match source must be one of: %s", strings.Join(models.CanaryMatchSources, ", "))
	ErrCanaryMatchOperator    = fmt.Errorf("match operator must be one of: %s", strings.Join(models.CanaryMatchOperators, ", "))
	// An ip rule reads the client address and needs no name.
	ErrCanaryMatchName  = errors.New("match rules on a header, query or cookie need a name")
	ErrCanaryMatchValue = errors.New("match rules need a value")
	// ErrCanaryPriority bounds the tie-breaker to a sane range.
	ErrCanaryPriority = errors.New("canary priority must be between 0 and 1000")
)

// The gateway fails such a rule closed at request time, so it is caught at save instead.
type ErrCanaryMatchRegex struct {
	Pattern string
	Err     error
}

func (e *ErrCanaryMatchRegex) Error() string {
	return fmt.Sprintf("match pattern %q is not a valid regular expression: %v", e.Pattern, e.Err)
}

func (e *ErrCanaryMatchRegex) Unwrap() error { return e.Err }

// An ip rule is only as trustworthy as the gateway's trusted-proxy setting.
const GomaProxyDocsURL = "https://goma.jkaninda.dev/usermanual/running-behind-a-proxy.html"

type CanaryRouting struct {
	Mode      models.CanaryMode
	Exclusive bool
	Priority  int
	Match     []models.CanaryMatchRule
}

// Rules never shift the traffic weight; returned warnings are advisory, the save happened.
func (s *Service) SetCanaryRouting(ctx context.Context, app *models.Application, in CanaryRouting) ([]string, error) {
	routing, err := normalizeCanaryRouting(in)
	if err != nil {
		return nil, err
	}
	if err := validateCanaryRouting(app, routing); err != nil {
		return nil, err
	}
	if err := s.apps.SetCanaryRouting(app.ID, routing.Mode, routing.Exclusive, routing.Priority, routing.Match); err != nil {
		return nil, err
	}
	app.CanaryMode, app.CanaryExclusive = routing.Mode, routing.Exclusive
	app.CanaryPriority, app.CanaryMatch = routing.Priority, routing.Match
	// At 0% an exclusive hold now means no traffic, so restart at the initial weight to resume the ramp.
	if routing.Mode == models.CanaryModeAuto && app.CanaryReleaseID != nil && app.CanaryWeight <= 0 {
		if err := s.apps.SetCanary(app.ID, app.CanaryReleaseID, app.CanaryInitialWeight); err == nil {
			app.CanaryWeight = app.CanaryInitialWeight
		}
	}
	if s.routeSync != nil {
		_ = s.routeSync.SyncRoute(ctx, app.ID)
	}
	s.emit(app, models.EventSettingsUpdated, canaryRoutingSummary(routing))
	return canaryRoutingWarnings(routing), nil
}

func normalizeCanaryRouting(in CanaryRouting) (CanaryRouting, error) {
	out := CanaryRouting{Mode: in.Mode, Exclusive: in.Exclusive, Priority: in.Priority}
	if out.Mode == "" {
		out.Mode = models.CanaryModeAuto
	}
	if !models.ValidCanaryMode(out.Mode) {
		return out, ErrCanaryModeInvalid
	}
	for _, r := range in.Match {
		out.Match = append(out.Match, models.CanaryMatchRule{
			Source:   strings.ToLower(strings.TrimSpace(r.Source)),
			Name:     strings.TrimSpace(r.Name),
			Operator: strings.ToLower(strings.TrimSpace(r.Operator)),
			Value:    strings.TrimSpace(r.Value),
		})
	}
	// Exclusive and priority are meaningless without rules.
	if out.Mode == models.CanaryModeAuto {
		out.Exclusive, out.Priority = false, 0
	}
	return out, nil
}

func validateCanaryRouting(app *models.Application, in CanaryRouting) error {
	if in.Mode == models.CanaryModeAuto && len(in.Match) > 0 {
		return ErrCanaryRulesNeedManual
	}
	if in.Priority < 0 || in.Priority > 1000 {
		return ErrCanaryPriority
	}
	if len(in.Match) > maxCanaryMatchRules {
		return ErrCanaryTooManyRules
	}
	if in.Exclusive && len(in.Match) == 0 {
		return ErrCanaryExclusiveNoMatch
	}
	if !in.Exclusive && len(in.Match) > 0 && effectiveCanaryWeight(app) <= 0 {
		return ErrCanaryPooledZeroWeight
	}
	for _, r := range in.Match {
		if err := validateCanaryMatchRule(r); err != nil {
			return err
		}
	}
	return nil
}

// Must check exactly what the gateway can evaluate, including that regexes compile.
func validateCanaryMatchRule(r models.CanaryMatchRule) error {
	if !models.ValidCanaryMatchSource(r.Source) {
		return ErrCanaryMatchSource
	}
	if !models.ValidCanaryMatchOperator(r.Operator) {
		return ErrCanaryMatchOperator
	}
	if r.Source != models.CanaryMatchSourceIP && r.Name == "" {
		return ErrCanaryMatchName
	}
	if r.Value == "" {
		return ErrCanaryMatchValue
	}
	if r.Operator == models.CanaryMatchOpRegex {
		if _, err := regexp.Compile(r.Value); err != nil {
			return &ErrCanaryMatchRegex{Pattern: r.Value, Err: err}
		}
	}
	return nil
}

// With no running canary, uses the next rollout's start weight so saved rules are judged realistically.
func effectiveCanaryWeight(app *models.Application) int {
	if app.CanaryReleaseID != nil {
		return app.CanaryWeight
	}
	return app.CanaryInitialWeight
}

func canaryRoutingWarnings(in CanaryRouting) []string {
	var out []string
	for _, r := range in.Match {
		if r.Source == models.CanaryMatchSourceIP {
			// With no trusted proxies configured, the client IP is attacker-selectable via a header.
			out = append(out, "This canary routes on client IP. Unless the gateway has proxy.trustedProxies configured, "+
				"the client address is whatever the caller claims, so anyone can put themselves in the canary. "+
				"See "+GomaProxyDocsURL)
			break
		}
	}
	return out
}

// canaryRoutingSummary is the activity-log line for a routing change.
func canaryRoutingSummary(in CanaryRouting) string {
	if in.Mode == models.CanaryModeAuto {
		return "Canary set to automatic ramp"
	}
	if len(in.Match) == 0 {
		return "Canary set to manual weight"
	}
	kind := "weighted"
	if in.Exclusive {
		kind = "exclusive"
	}
	return fmt.Sprintf("Canary set to manual with %d %s match rule(s)", len(in.Match), kind)
}
