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

package routes

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
)

func TestRoutePath(t *testing.T) {
	app := okapi.New()
	v1 := app.Group("/api/v1")
	ws := v1.Group("/workspaces")

	for _, tc := range []struct {
		group *okapi.Group
		path  string
		want  string
	}{
		{ws, "/{workspace}/apps", "/api/v1/workspaces/{workspace}/apps"},
		{ws, "{workspace}/apps", "/api/v1/workspaces/{workspace}/apps"},
		{v1, "/me", "/api/v1/me"},
		{ws, "", "/api/v1/workspaces"},
		{nil, "/healthz", "/healthz"},
	} {
		got := routePath(&okapi.RouteDefinition{Group: tc.group, Path: tc.path})
		if got != tc.want {
			t.Errorf("routePath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// register must attach the guard to every definition it is handed — that is the whole mechanism,
// and a definition that slipped through would be silently unguarded.
func TestRegisterAttachesTheScopeGuard(t *testing.T) {
	app := okapi.New()
	r := &Router{app: app, scopeMode: middlewares.ScopeModeEnforce}
	v1 := app.Group("/api/v1")

	noop := func(c *okapi.Context) error { return c.Next() }
	defs := []okapi.RouteDefinition{
		{Method: http.MethodGet, Path: "/a", Group: v1, Handler: noop},
		{Method: http.MethodPost, Path: "/b", Group: v1, Middlewares: []okapi.Middleware{noop}, Handler: noop},
	}
	before := []int{len(defs[0].Middlewares), len(defs[1].Middlewares)}

	r.register(defs...)

	for i, def := range defs {
		// The scope guard and the suspension guard.
		if len(def.Middlewares) != before[i]+2 {
			t.Errorf("def %d: middlewares %d → %d, want exactly two appended", i, before[i], len(def.Middlewares))
		}
	}
}

// The guarantee the fix rests on: every route reaches the table through register, which is what
// makes the scope guard impossible to forget on a route added later. A direct r.app.Register call
// would register a route with no guard at all and nothing else would notice.
func TestNoRouteBypassesRegister(t *testing.T) {
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	bypass := regexp.MustCompile(`r\.app\.Register\(`)
	if locs := bypass.FindAllIndex(src, -1); len(locs) > 0 {
		var lines []string
		for _, loc := range locs {
			lines = append(lines, "line "+itoa(1+strings.Count(string(src[:loc[0]]), "\n")))
		}
		t.Fatalf("routes registered outside register(), so they carry no scope guard: %s", strings.Join(lines, ", "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
