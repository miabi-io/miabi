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

import "testing"

func TestSafeNext(t *testing.T) {
	for raw, want := range map[string]string{
		"/?workspace=prod":       "/?workspace=prod",
		"/apps/12?tab=logs#top":  "/apps/12?tab=logs#top",
		" /dashboard ":           "/dashboard",
		"":                       "",
		"dashboard":              "",
		"//evil.example/x":       "",
		"/\\evil.example":        "",
		"https://evil.example/":  "",
		"javascript:alert(1)":    "",
		"/ok\r\nSet-Cookie: x=1": "",
		"/%2F%2Fevil.example":    "/%2F%2Fevil.example",
	} {
		if got := SafeNext(raw); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", raw, got, want)
		}
	}
}
