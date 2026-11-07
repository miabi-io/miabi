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

package handlers

import (
	"net/http"
	"net/url"
	"strings"
)

// Admits requests with no Origin (agents, runners, CLIs); a "*" entry disables the check.
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
