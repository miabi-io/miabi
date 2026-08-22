// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// maxCanaryMatchRules bounds a rule set. The gateway ANDs every rule, so long
// sets are almost always a mistake rather than intent; the cap keeps a bad
// payload from bloating the rendered route.
const maxCanaryMatchRules = 10

// Canary routing validation. Each of these is a configuration that looks right
// and sends traffic somewhere surprising, so each gets its own refusal rather
// than a generic "invalid".
var (
	ErrCanaryModeInvalid = errors.New("canary mode must be auto or manual")
	// ErrCanaryRulesNeedManual rejects rules in automatic mode: the ramp owns the
	// weight there, and automatic mode is defined as the empty rule set.
	ErrCanaryRulesNeedManual = errors.New("canary match rules require manual mode")
	// ErrCanaryExclusiveNoMatch rejects exclusive without rules — the gateway
	// would hand the canary every request, which is almost never intended.
	ErrCanaryExclusiveNoMatch = errors.New("an exclusive canary needs at least one match rule; without one it takes all traffic")
	// ErrCanaryPooledZeroWeight rejects a non-exclusive rule set at weight 0: the
	// canary joins the pool with no share, so matching requests still all go to
	// stable and the rules look active while doing nothing.
	ErrCanaryPooledZeroWeight = errors.New("a canary at 0% needs exclusive routing; otherwise its match rules send no traffic")
	ErrCanaryTooManyRules     = fmt.Errorf("a canary may have at most %d match rules", maxCanaryMatchRules)
	ErrCanaryMatchSource      = fmt.Errorf("match source must be one of: %s", strings.Join(models.CanaryMatchSources, ", "))
	ErrCanaryMatchOperator    = fmt.Errorf("match operator must be one of: %s", strings.Join(models.CanaryMatchOperators, ", "))
	// ErrCanaryMatchName is returned for a header/query/cookie rule with no key.
	// An ip rule reads the client address and needs no name.
	ErrCanaryMatchName  = errors.New("match rules on a header, query or cookie need a name")
	ErrCanaryMatchValue = errors.New("match rules need a value")
	// ErrCanaryPriority bounds the tie-breaker to a sane range.
	ErrCanaryPriority = errors.New("canary priority must be between 0 and 1000")
)

// ErrCanaryMatchRegex reports a regex rule whose pattern does not compile. The
// gateway logs and fails such a rule closed at request time, so it is caught at
// save instead — a pattern that never matches is invisible in production.
type ErrCanaryMatchRegex struct {
	Pattern string
	Err     error
}

func (e *ErrCanaryMatchRegex) Error() string {
	return fmt.Sprintf("match pattern %q is not a valid regular expression: %v", e.Pattern, e.Err)
}

func (e *ErrCanaryMatchRegex) Unwrap() error { return e.Err }

// GomaProxyDocsURL documents configuring the gateway's trusted proxies. Linked
// from the client-IP warning, because an ip rule is only as trustworthy as that
// setting.
const GomaProxyDocsURL = "https://goma.jkaninda.dev/usermanual/running-behind-a-proxy.html"

// CanaryRouting is the user-settable steering for an app's canary: who moves the
// weight, and which requests the canary is eligible for.
type CanaryRouting struct {
	Mode      models.CanaryMode
	Exclusive bool
	Priority  int
	Match     []models.CanaryMatchRule
}

// SetCanaryRouting validates and stores the app's canary steering, then re-syncs
// the route so a rule change takes effect immediately — no redeploy. The traffic
// weight is not touched: rules never shift traffic as a side effect of saving.
//
// Returned warnings are advisory (the save happened); an error means nothing was
// written.
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
	// Handing a running canary back to the ramp: an exclusive rollout may have
	// been held at 0%, which now means no traffic at all rather than "rules only".
	// Restart it at the app's initial weight so the ramp resumes from somewhere
	// real instead of leaving the canary dark until the next tick.
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

// normalizeCanaryRouting trims and defaults the payload, and drops rules that
// automatic mode is not allowed to carry anyway.
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
	// Automatic mode is the same model with the rule set empty; exclusive and
	// priority are meaningless without rules.
	if out.Mode == models.CanaryModeAuto {
		out.Exclusive, out.Priority = false, 0
	}
	return out, nil
}

// validateCanaryRouting rejects rule sets that would route somewhere other than
// where they read as routing.
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

// validateCanaryMatchRule checks one rule against exactly what the gateway can
// evaluate: a known source and operator, the key the source needs, and — for
// regex — a pattern that actually compiles.
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

// effectiveCanaryWeight is the share the canary holds now, or the share the next
// rollout will start at when none is running — so a rule set saved ahead of a
// canary is judged against the weight it will actually have.
func effectiveCanaryWeight(app *models.Application) int {
	if app.CanaryReleaseID != nil {
		return app.CanaryWeight
	}
	return app.CanaryInitialWeight
}

// canaryRoutingWarnings returns advisory notes for a saved rule set: things that
// are valid but need something outside Miabi to be true.
func canaryRoutingWarnings(in CanaryRouting) []string {
	var out []string
	for _, r := range in.Match {
		if r.Source == models.CanaryMatchSourceIP {
			// Behind a CDN or load balancer the client IP is whatever the proxy
			// reports — and with no trusted proxies configured, whatever the caller
			// reports. An ip rule is then attacker-selectable: anyone can put
			// themselves in the canary by setting a header.
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
