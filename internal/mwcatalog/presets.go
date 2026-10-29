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
		Key:         "sso",
		DisplayName: "Single sign-on",
		Description: "Sign users in at the gateway. Fill in your provider's issuer and client credentials.",
		Type:        "oidc",
		Rule: map[string]any{
			"scopes":       []any{"openid", "email", "profile"},
			"callbackPath": "/oauth2/callback",
			"logoutPath":   "/oauth2/logout",
			"pkce":         true,
			"session":      map[string]any{"store": "cookie", "ttl": "12h", "idleTimeout": "1h"},
			"forward": map[string]any{
				"headers": map[string]any{
					"X-Auth-User":  "sub",
					"X-Auth-Email": "email",
				},
				"stripInbound": true,
			},
		},
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
		Key:         "redirect-to-www",
		DisplayName: "Redirect to www",
		Description: "Send example.com to www.example.com, keeping the path. The route must serve both names.",
		Type:        "redirectHost",
		Rule:        map[string]any{"www": "add", "permanent": true},
	},
	{
		Key:         "redirect-to-apex",
		DisplayName: "Redirect www to the bare domain",
		Description: "Send www.example.com to example.com, keeping the path. The route must serve both names.",
		Type:        "redirectHost",
		Rule:        map[string]any{"www": "remove", "permanent": true},
	},
	{
		Key:         "hsts",
		DisplayName: "HSTS",
		Description: "Tell browsers to use HTTPS only for this host for a year.",
		Type:        "responseHeaders",
		Rule:        map[string]any{"setHeaders": map[string]any{"Strict-Transport-Security": "max-age=31536000"}},
	},
	{
		Key:         "noindex",
		DisplayName: "Hide from search engines",
		Description: "Ask crawlers not to index or follow the site — for staging and preview hosts.",
		Type:        "responseHeaders",
		Rule:        map[string]any{"setHeaders": map[string]any{"X-Robots-Tag": "noindex, nofollow"}},
	},
	{
		Key:         "compression",
		DisplayName: "Compress responses",
		Description: "Compress HTML, JSON, CSS and JavaScript with zstd, Brotli or gzip, whichever the client supports.",
		Type:        "compress",
		Rule:        map[string]any{},
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

// defaultSeed is the curated set of policies a new workspace starts with. All
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
	{
		Name:        "compression",
		DisplayName: "Compress responses",
		Type:        "compress",
		Rule:        map[string]any{},
	},
	{
		Name:        "redirect-to-www",
		DisplayName: "Redirect to www",
		Type:        "redirectHost",
		Rule:        map[string]any{"www": "add", "permanent": true},
	},
	{
		Name:        "redirect-to-apex",
		DisplayName: "Redirect www to the bare domain",
		Type:        "redirectHost",
		Rule:        map[string]any{"www": "remove", "permanent": true},
	},
	{
		Name:        "hsts",
		DisplayName: "HSTS",
		Type:        "responseHeaders",
		Rule:        map[string]any{"setHeaders": map[string]any{"Strict-Transport-Security": "max-age=31536000"}},
	},
	{
		Name:        "rate-limit",
		DisplayName: "Rate limit (100/min)",
		Type:        "rateLimit",
		Rule:        map[string]any{"unit": "minute", "requestsPerUnit": 100, "burst": 50, "banDuration": "10m"},
	},
	{
		Name:        "noindex",
		DisplayName: "Hide from search engines",
		Type:        "responseHeaders",
		Rule:        map[string]any{"setHeaders": map[string]any{"X-Robots-Tag": "noindex, nofollow"}},
	},
}

// DefaultSeed returns the default middlewares a new workspace is seeded with.
func DefaultSeed() []SeedMiddleware {
	out := make([]SeedMiddleware, len(defaultSeed))
	copy(out, defaultSeed)
	return out
}
