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
package middlewares

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jkaninda/okapi"
)

func TestRestrictionExempt(t *testing.T) {
	for _, tt := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodDelete, "/api/v1/workspaces/acme/members/12", true},
		{http.MethodDelete, "/api/v1/workspaces/acme", true},
		{http.MethodPost, "/api/v1/workspaces/acme/apps/web/stop", true},
		{http.MethodPost, "/api/v1/workspaces/acme/backup/export", true},
		{http.MethodPost, "/api/v1/workspaces/acme/apps", false},
		{http.MethodDelete, "/api/v1/workspaces/acme/apps/web", false},
		{http.MethodPost, "/api/v1/workspaces/acme/apps/web/start", false},
		{http.MethodPatch, "/api/v1/workspaces/acme/members/12", false},
	} {
		if got := restrictionExempt(tt.method, tt.path); got != tt.want {
			t.Errorf("%s %s = %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}

func TestSuspensionGuard(t *testing.T) {
	call := func(method, path, reason string) (int, string) {
		app := okapi.New()
		seed := func(c *okapi.Context) error {
			if reason != "" {
				c.Set(CtxSuspended, reason)
			}
			return c.Next()
		}
		h := func(c *okapi.Context) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) }
		app.Handle(method, path, h, okapi.UseMiddleware(seed), okapi.UseMiddleware(SuspensionGuard()))
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code, rec.Body.String()
	}
	for _, tt := range []struct {
		name, method, path, reason string
		want                       int
	}{
		{"not suspended writes", http.MethodPost, "/api/v1/workspaces", "", http.StatusOK},
		{"suspended reads", http.MethodGet, "/api/v1/workspaces", "unpaid", http.StatusOK},
		{"suspended signs out", http.MethodPost, "/api/v1/auth/logout", "unpaid", http.StatusOK},
		{"suspended write refused", http.MethodPost, "/api/v1/workspaces", "unpaid", http.StatusForbidden},
	} {
		code, body := call(tt.method, tt.path, tt.reason)
		if code != tt.want {
			t.Errorf("%s: %d %s", tt.name, code, body)
		}
		if tt.want == http.StatusForbidden && !strings.Contains(body, "unpaid") {
			t.Errorf("%s: the reason must reach the user: %s", tt.name, body)
		}
	}
}
