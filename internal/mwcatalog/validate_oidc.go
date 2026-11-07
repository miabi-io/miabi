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

import (
	"fmt"
	"time"
)

var knownProviders = map[string]bool{
	"google": true, "github": true, "gitlab": true, "amazon": true, "facebook": true,
}

var claimSources = map[string]bool{"id_token": true, "userinfo": true, "access_token": true}

// Mirrors the gateway's load-time rules so a policy that cannot guard a route is refused before deploy.
func validateOIDC(rule map[string]any) error {
	issuer := str(rule, "issuer")
	provider := str(rule, "provider")
	endpoint, _ := rule["endpoint"].(map[string]any)

	authURL := str(endpoint, "authUrl")
	tokenURL := str(endpoint, "tokenUrl")
	jwksURL := str(endpoint, "jwksUrl")
	userInfoURL := str(endpoint, "userInfoUrl")
	known := knownProviders[provider]

	if issuer == "" && !known && (authURL == "" || tokenURL == "") {
		return fmt.Errorf("%w: set an issuer, choose a known provider, or fill in endpoint.authUrl and endpoint.tokenUrl", ErrInvalidRule)
	}
	// Without one the gateway cannot tell a real token from a forged one and refuses to guard the route.
	if issuer == "" && !known && jwksURL == "" && userInfoURL == "" {
		return fmt.Errorf("%w: set an issuer, or endpoint.jwksUrl or endpoint.userInfoUrl, so tokens can be verified", ErrInvalidRule)
	}

	for _, src := range strList(rule, "claimsSource") {
		if !claimSources[src] {
			return fmt.Errorf("%w: unknown claim source %q, expected id_token, userinfo or access_token", ErrInvalidRule, src)
		}
	}

	session, _ := rule["session"].(map[string]any)
	if session == nil {
		return nil
	}
	switch store := str(session, "store"); store {
	case "", "cookie", "memory", "redis":
	default:
		return fmt.Errorf("%w: unknown session store %q, expected cookie, memory or redis", ErrInvalidRule, store)
	}
	for _, key := range []string{"ttl", "idleTimeout"} {
		v := str(session, key)
		if v == "" {
			continue
		}
		if _, err := time.ParseDuration(v); err != nil {
			return fmt.Errorf("%w: session.%s %q is not a duration, e.g. 12h or 30m", ErrInvalidRule, key, v)
		}
	}
	cookie, _ := session["cookie"].(map[string]any)
	switch ss := str(cookie, "sameSite"); ss {
	case "", "lax", "strict", "none":
	default:
		return fmt.Errorf("%w: unknown session.cookie.sameSite %q, expected lax, strict or none", ErrInvalidRule, ss)
	}
	return nil
}

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func strList(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
