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
	"testing"

	"github.com/jkaninda/okapi"
)

// refusalOf runs check inside a real route so c.Param resolves, and returns what it refused.
func refusalOf(t *testing.T, method, pattern, path string, check func(c *okapi.Context) error) error {
	t.Helper()
	var got error
	app := okapi.New()
	app.Handle(method, pattern, func(c *okapi.Context) error {
		got = check(c)
		return c.JSON(http.StatusOK, nil)
	})
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
	return got
}

// Refusals are returned, never written: an aborted response returns nil and the caller would continue.
func TestEphemeralKeyRefusal(t *testing.T) {
	apps := fakeApps{}
	check := func(bound uint) func(c *okapi.Context) error {
		return func(c *okapi.Context) error { return ephemeralKeyRefusal(c, bound, apps, 0) }
	}
	if err := refusalOf(t, http.MethodPost, "/apps/{appID}/deploy", "/apps/7/deploy", check(7)); err != nil {
		t.Fatalf("its own app = %v, want allowed", err)
	}
	if err := refusalOf(t, http.MethodPost, "/apps/{appID}/deploy", "/apps/8/deploy", check(7)); err == nil {
		t.Fatal("another app was allowed")
	}
	if err := refusalOf(t, http.MethodGet, "/workspaces", "/workspaces", check(7)); err == nil {
		t.Fatal("a route with no app was allowed")
	}
	if err := refusalOf(t, http.MethodPost, "/apps/{appID}/deploy", "/apps/7/deploy", check(0)); err == nil {
		t.Fatal("an unbound job credential was allowed")
	}
}

func TestWorkspaceKeyRefusal(t *testing.T) {
	if err := refusalOf(t, http.MethodDelete, "/api/v1/workspaces/{workspace}/apps/1", "/api/v1/workspaces/3/apps/1", workspaceKeyRefusal); err != nil {
		t.Fatalf("a workspace route = %v, want allowed", err)
	}
	if err := refusalOf(t, http.MethodPost, "/api/v1/account/tokens", "/api/v1/account/tokens", workspaceKeyRefusal); err == nil {
		t.Fatal("an account-level write was allowed")
	}
}
