// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"net/http"
	"net/url"
	"strings"
)

// allowWSOrigin builds a websocket CheckOrigin that blocks cross-site WebSocket hijacking while
// still admitting non-browser clients: requests with no Origin (agents, runners, CLIs) and
// browser requests same-origin with Host or in the allowlist. A "*" entry disables the check.
func allowWSOrigin(allowed []string) func(*http.Request) bool {
	allowSet := make(map[string]struct{}, len(allowed))
	wildcard := false
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if a == "*" {
			wildcard = true
			continue
		}
		if a != "" {
			allowSet[normOrigin(a)] = struct{}{}
		}
	}
	return func(r *http.Request) bool {
		if wildcard {
			return true
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser client (no ambient credentials to hijack)
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false
		}
		if strings.EqualFold(u.Host, r.Host) {
			return true // same-origin
		}
		_, ok := allowSet[normOrigin(origin)]
		return ok
	}
}

func normOrigin(o string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))
}
