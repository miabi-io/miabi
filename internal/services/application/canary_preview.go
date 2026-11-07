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
	// Eligible for both, so the gateway picks by weight and no single answer is truthful.
	CanaryPreviewSplit = "split"
)

// Header names match case-insensitively; query and cookie names match exactly, as the gateway reads them.
type CanaryPreviewRequest struct {
	Headers map[string]string `json:"headers,omitempty"`
	Query   map[string]string `json:"query,omitempty"`
	Cookies map[string]string `json:"cookies,omitempty"`
	IP      string            `json:"ip,omitempty"`
}

type CanaryPreviewRule struct {
	Rule    models.CanaryMatchRule `json:"rule"`
	Actual  string                 `json:"actual"`
	Matched bool                   `json:"matched"`
}

// CanaryPreview answers "which backend would serve this request?".
type CanaryPreview struct {
	Backend string `json:"backend"` // stable | canary | split
	// 0, 100, or the weight when the request lands in the weighted pool.
	CanaryChance int                 `json:"canary_chance"`
	Matched      bool                `json:"matched"` // every rule held
	Reason       string              `json:"reason"`
	Rules        []CanaryPreviewRule `json:"rules"`
}

// PreviewCanaryRouting mirrors the gateway's selection and is pure: no deploy or gateway needed.
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

// The gateway reads headers through Go's canonicalizing http.Header.
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

// Must mirror the gateway exactly: an unknown operator never matches there, so never here.
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
