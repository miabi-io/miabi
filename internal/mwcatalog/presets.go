// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package mwcatalog

// Preset is a named, pre-filled policy the UI can instantiate in one click. Its
// Rule is a sensible starting point; the user can tweak it (and must supply any
// secret or empty required field, e.g. basicAuth users or accessPolicy ranges).
type Preset struct {
	Key         string         `json:"key"`
	DisplayName string         `json:"display_name"`
	Description string         `json:"description"`
	Type        string         `json:"type"`
	Rule        map[string]any `json:"rule"`
}

var presets = []Preset{
	{
		Key:         "force-https",
		DisplayName: "Force HTTPS",
		Description: "Redirect all HTTP requests to HTTPS (permanent).",
		Type:        "redirectScheme",
		Rule:        map[string]any{"scheme": "https", "permanent": true},
	},
	{
		Key:         "rate-limit",
		DisplayName: "Rate-limit (100/min)",
		Description: "Throttle clients to 100 requests per minute.",
		Type:        "rateLimit",
		Rule:        map[string]any{"unit": "minute", "requestsPerUnit": 100, "burst": 50, "banDuration": "10m"},
	},
	{
		Key:         "basic-auth",
		DisplayName: "Protect with basic auth",
		Description: "Require a username and password. Add your users after applying.",
		Type:        "basicAuth",
		Rule:        map[string]any{"realm": "Restricted", "users": []any{}},
	},
	{
		Key:         "ip-allowlist",
		DisplayName: "IP allowlist",
		Description: "Allow only the IP ranges you specify; deny everything else.",
		Type:        "accessPolicy",
		Rule:        map[string]any{"action": "ALLOW", "sourceRanges": []any{}},
	},
	{
		Key:         "geo-allowlist",
		DisplayName: "Country allowlist",
		Description: "Allow only the countries you specify; block the rest. Needs a GeoIP database on the gateway.",
		Type:        "geoBlock",
		Rule:        map[string]any{"action": "ALLOW", "countries": []any{}, "allowUnknown": true},
	},
	{
		Key:         "body-limit",
		DisplayName: "Limit request body (10MB)",
		Description: "Reject requests with a body larger than 10MB.",
		Type:        "bodyLimit",
		Rule:        map[string]any{"limit": "10MB"},
	},
	{
		Key:         "block-access",
		DisplayName: "Block access",
		Description: "Deny all requests to the matched paths (HTTP 403).",
		Type:        "access",
		Rule:        map[string]any{"statusCode": 403},
	},
	{
		Key:         "block-bad-bots",
		DisplayName: "Block common bots",
		Description: "Reject requests from common crawlers and scripted clients.",
		Type:        "userAgentBlock",
		Rule:        map[string]any{"userAgents": []any{"AhrefsBot", "SemrushBot", "MJ12bot", "python-requests", "curl"}},
	},
	{
		Key:         "redirect-to-url",
		DisplayName: "Redirect to a URL",
		Description: "Send all matched requests to a fixed destination URL. Set the URL after applying.",
		Type:        "redirect",
		Rule:        map[string]any{"url": "", "permanent": true},
	},
}

// Presets returns the named one-click policy presets.
func Presets() []Preset {
	out := make([]Preset, len(presets))
	copy(out, presets)
	return out
}

// SeedMiddleware is a default policy definition created for every new workspace.
// Name is the workspace-unique slug handle (and Goma name); DisplayName is the UI
// label. Seeded policies are inert until a route references them.
type SeedMiddleware struct {
	Name        string
	DisplayName string
	Type        string
	Rule        map[string]any
}

// defaultSeed is the curated set of policies a new workspace starts with. Both
// are safe (no effect until attached to a route) and broadly useful; keeping the
// set as data makes it trivial to tune.
var defaultSeed = []SeedMiddleware{
	{
		Name:        "security-headers",
		DisplayName: "Security headers",
		Type:        "responseHeaders",
		Rule: map[string]any{"setHeaders": map[string]any{
			"X-Frame-Options":        "DENY",
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "strict-origin-when-cross-origin",
		}},
	},
	{
		Name:        "force-https",
		DisplayName: "Force HTTPS",
		Type:        "redirectScheme",
		Rule:        map[string]any{"scheme": "https", "permanent": true},
	},
}

// DefaultSeed returns the default middlewares a new workspace is seeded with.
func DefaultSeed() []SeedMiddleware {
	out := make([]SeedMiddleware, len(defaultSeed))
	copy(out, defaultSeed)
	return out
}
