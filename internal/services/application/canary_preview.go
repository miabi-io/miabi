// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"fmt"
	"net/textproto"
	"regexp"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// Backends a preview can resolve to.
const (
	CanaryPreviewStable = "stable"
	CanaryPreviewCanary = "canary"
	// CanaryPreviewSplit means the request is eligible for both and the gateway
	// picks by weight, so no single answer is truthful.
	CanaryPreviewSplit = "split"
)

// CanaryPreviewRequest is the request attributes to evaluate the rules against.
// Header names are matched case-insensitively (as HTTP does); query and cookie
// names are matched exactly, as the gateway reads them.
type CanaryPreviewRequest struct {
	Headers map[string]string `json:"headers,omitempty"`
	Query   map[string]string `json:"query,omitempty"`
	Cookies map[string]string `json:"cookies,omitempty"`
	IP      string            `json:"ip,omitempty"`
}

// CanaryPreviewRule is one rule's verdict, with the value the gateway would have
// read — so a preview that surprises the user shows why.
type CanaryPreviewRule struct {
	Rule    models.CanaryMatchRule `json:"rule"`
	Actual  string                 `json:"actual"`
	Matched bool                   `json:"matched"`
}

// CanaryPreview answers "which backend would serve this request?".
type CanaryPreview struct {
	Backend string `json:"backend"` // stable | canary | split
	// CanaryChance is the percentage of such requests the canary would serve:
	// 0, 100, or the weight when the request lands in the weighted pool.
	CanaryChance int                 `json:"canary_chance"`
	Matched      bool                `json:"matched"` // every rule held
	Reason       string              `json:"reason"`
	Rules        []CanaryPreviewRule `json:"rules"`
}

// PreviewCanaryRouting resolves a hypothetical request against the app's canary
// configuration, mirroring the gateway's selection: an exclusive canary takes
// every matching request, a non-exclusive one joins the weighted pool, and a
// request matching nothing goes to stable. Pure — no deploy, no canary and no
// gateway required to run it.
func PreviewCanaryRouting(app *models.Application, req CanaryPreviewRequest) CanaryPreview {
	weight := effectiveCanaryWeight(app)
	if weight > 100 {
		weight = 100
	}
	if weight < 0 {
		weight = 0
	}

	out := CanaryPreview{Matched: true}
	for _, r := range app.CanaryMatch {
		actual := previewValue(req, r)
		matched := evaluateCanaryOperator(r.Operator, actual, r.Value)
		out.Rules = append(out.Rules, CanaryPreviewRule{Rule: r, Actual: actual, Matched: matched})
		if !matched {
			out.Matched = false
		}
	}

	// No rules: the canary is a plain weighted split and every request is eligible.
	if len(app.CanaryMatch) == 0 {
		if weight <= 0 {
			return stablePreview(out, "No canary traffic is configured.")
		}
		out.Backend, out.CanaryChance = CanaryPreviewSplit, weight
		out.Reason = fmt.Sprintf("No match rules, so every request is split by weight: %d%% canary, %d%% stable.", weight, 100-weight)
		return out
	}

	if !out.Matched {
		return stablePreview(out, "The request does not satisfy every match rule, so the canary is not eligible for it.")
	}
	if app.CanaryExclusive {
		out.Backend, out.CanaryChance = CanaryPreviewCanary, 100
		out.Reason = "Every match rule holds and the canary is exclusive, so it serves this request in full."
		return out
	}
	if weight <= 0 {
		return stablePreview(out, "Every match rule holds, but the canary is not exclusive and holds no weight, so stable still serves it.")
	}
	out.Backend, out.CanaryChance = CanaryPreviewSplit, weight
	out.Reason = fmt.Sprintf("Every match rule holds, so the request joins the weighted pool: %d%% canary, %d%% stable.", weight, 100-weight)
	return out
}

func stablePreview(out CanaryPreview, reason string) CanaryPreview {
	out.Backend, out.CanaryChance, out.Reason = CanaryPreviewStable, 0, reason
	return out
}

// previewValue reads the attribute a rule names, the way the gateway reads it.
func previewValue(req CanaryPreviewRequest, r models.CanaryMatchRule) string {
	switch r.Source {
	case models.CanaryMatchSourceHeader:
		return lookupHeader(req.Headers, r.Name)
	case models.CanaryMatchSourceQuery:
		return req.Query[r.Name]
	case models.CanaryMatchSourceCookie:
		return req.Cookies[r.Name]
	case models.CanaryMatchSourceIP:
		return req.IP
	}
	return ""
}

// lookupHeader resolves a header case-insensitively, since the gateway reads it
// through Go's canonicalizing http.Header.
func lookupHeader(headers map[string]string, name string) string {
	if v, ok := headers[name]; ok {
		return v
	}
	want := textproto.CanonicalMIMEHeaderKey(name)
	for k, v := range headers {
		if textproto.CanonicalMIMEHeaderKey(k) == want {
			return v
		}
	}
	return ""
}

// evaluateCanaryOperator mirrors the gateway's comparison table exactly. An
// unknown operator never matches there and must never match here; validation
// keeps one from being stored in the first place.
func evaluateCanaryOperator(op, actual, expected string) bool {
	switch op {
	case models.CanaryMatchOpEquals:
		return actual == expected
	case models.CanaryMatchOpNotEquals:
		return actual != expected
	case models.CanaryMatchOpContains:
		return strings.Contains(actual, expected)
	case models.CanaryMatchOpNotContains:
		return !strings.Contains(actual, expected)
	case models.CanaryMatchOpStartsWith:
		return strings.HasPrefix(actual, expected)
	case models.CanaryMatchOpEndsWith:
		return strings.HasSuffix(actual, expected)
	case models.CanaryMatchOpRegex:
		ok, err := regexp.MatchString(expected, actual)
		return err == nil && ok
	case models.CanaryMatchOpIn:
		for _, v := range strings.Split(expected, ",") {
			if strings.TrimSpace(v) == actual {
				return true
			}
		}
		return false
	}
	return false
}
