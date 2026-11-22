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

package route

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type traceCase struct {
	in      flowInput
	mws     map[string]*models.Middleware
	req     TraceRequest
	kind    string
	status  int
	backend string
}

func traceFixture() (flowInput, map[string]*models.Middleware) {
	in := flowFixture()
	in.rt.Rewrite = "/"
	in.rt.Middlewares = []string{"force-https", "api-auth", "admin-only"}
	list := []models.Middleware{
		{Name: "force-https", Type: "redirectScheme", Rule: map[string]any{"scheme": "https", "permanent": true}},
		{Name: "api-auth", Type: "jwtAuth", Paths: []string{"/.*"}},
		{Name: "admin-only", Type: "access", Paths: []string{"/internal/*"}},
	}
	in.middlewares = list
	in.routes = []models.Route{*in.rt, {ID: 2, Name: "admin", ApplicationID: 8, Path: "/api/admin", Hosts: []string{"api.example.com"}}}
	mws := map[string]*models.Middleware{}
	for i := range list {
		mws[list[i].Name] = &list[i]
	}
	return in, mws
}

func runTrace(t *testing.T, name string, c traceCase) *Trace {
	t.Helper()
	tr := buildTrace(buildFlow(c.in), c.in.app, c.mws, c.req)
	if tr.Result.Kind != c.kind || tr.Result.Status != c.status || (c.backend != "" && tr.Result.Backend != c.backend) {
		t.Errorf("%s: result = %+v, want kind %s status %d backend %q", name, tr.Result, c.kind, c.status, c.backend)
		for _, s := range tr.Steps {
			t.Logf("  %s %s %s: %s", s.Stage, s.Code, s.Outcome, s.Detail)
		}
	}
	return tr
}

var bearer = map[string]string{"authorization": "Bearer abc"}

func TestTraceRouteSelection(t *testing.T) {
	in, mws := traceFixture()
	tr := runTrace(t, "redirect", traceCase{in: in, mws: mws, req: TraceRequest{Scheme: "http", Path: "/api/orders"}, kind: "redirect", status: 301})
	if tr.Result.Location != "https://api.example.com/api/orders" {
		t.Errorf("location = %q", tr.Result.Location)
	}
	tr = runTrace(t, "sibling", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api/admin/users"}, kind: "other_route"})
	if tr.Result.RouteName != "admin" {
		t.Errorf("other route = %q", tr.Result.RouteName)
	}
	runTrace(t, "not under path", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/apiv2"}, kind: "no_route"})
	runTrace(t, "foreign host", traceCase{in: in, mws: mws, req: TraceRequest{Host: "other.example.com", Path: "/api"}, kind: "no_route"})

	in.rt.Methods = []string{"GET"}
	runTrace(t, "method", traceCase{in: in, mws: mws, req: TraceRequest{Method: "POST", Path: "/api/orders", Headers: bearer}, kind: "no_route"})
}

func TestTraceChainAndApp(t *testing.T) {
	in, mws := traceFixture()
	runTrace(t, "no token", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api/orders"}, kind: "early_answer", status: 401})
	tr := runTrace(t, "token", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api/orders/42?x=1", Headers: bearer}, kind: "app", backend: "stable"})
	if tr.Result.UpstreamPath != "/orders/42" {
		t.Errorf("upstream = %q", tr.Result.UpstreamPath)
	}
	var skipped bool
	for _, s := range tr.Steps {
		if s.Code == "mw_out_of_scope" && s.Params["name"] == "admin-only" && s.Index != nil && *s.Index == 2 {
			skipped = true
		}
	}
	if !skipped {
		t.Error("admin-only should be out of scope for /api/orders/42")
	}
	runTrace(t, "blocked path", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api/internal/x", Headers: bearer}, kind: "early_answer", status: 403})
}

func TestTraceCanary(t *testing.T) {
	in, mws := traceFixture()
	in.app.CanaryReleaseID = uintp(2)
	in.app.CanaryWeight = 10
	in.app.CanaryExclusive = true
	in.app.CanaryMatch = []models.CanaryMatchRule{{Source: "header", Name: "X-Canary", Operator: "equals", Value: "1"}}
	headers := map[string]string{"Authorization": "Bearer abc", "x-canary": "1"}
	runTrace(t, "rule", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api", Headers: headers}, kind: "app", backend: "canary"})
	runTrace(t, "no rule", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api", Headers: bearer}, kind: "app", backend: "stable"})

	in.app.CanaryExclusive = false
	in.app.CanaryMatch = []models.CanaryMatchRule{{Source: "query", Name: "beta", Operator: "equals", Value: "on"}}
	tr := runTrace(t, "query rule", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api?beta=on", Headers: bearer}, kind: "app", backend: "split"})
	if tr.Result.CanaryChance != 10 {
		t.Errorf("chance = %d", tr.Result.CanaryChance)
	}
}

func TestTraceStates(t *testing.T) {
	in, mws := traceFixture()
	in.rt.Maintenance = models.RouteMaintenance{Enabled: true, StatusCode: 503, HTML: "<h1>x</h1>"}
	tr := runTrace(t, "maintenance", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api", Headers: map[string]string{"Accept": "text/html,*/*"}}, kind: "maintenance", status: 503})
	if last := tr.Steps[len(tr.Steps)-1]; last.Params["body"] != "page" {
		t.Errorf("maintenance body = %q", last.Params["body"])
	}

	in, mws = traceFixture()
	in.domains = []models.Domain{{Name: "example.com"}}
	runTrace(t, "unverified", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api"}, kind: "not_served", status: 404})

	in, mws = traceFixture()
	in.health = func(*models.Release) *models.ReleaseHealth { return &models.ReleaseHealth{Status: "exited", Desired: 1} }
	runTrace(t, "no replica", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api", Headers: bearer}, kind: "no_replica", status: 502})
}

func TestTraceIPPolicy(t *testing.T) {
	in, mws := traceFixture()
	in.rt.Middlewares = []string{"office"}
	office := &models.Middleware{Name: "office", Type: "accessPolicy", Rule: map[string]any{"action": "ALLOW", "sourceRanges": []any{"10.0.0.0/8"}}}
	in.middlewares = []models.Middleware{*office}
	mws = map[string]*models.Middleware{"office": office}
	runTrace(t, "inside", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api", IP: "10.1.2.3"}, kind: "app"})
	runTrace(t, "outside", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api", IP: "203.0.113.9"}, kind: "early_answer", status: 403})
	runTrace(t, "unknown", traceCase{in: in, mws: mws, req: TraceRequest{Path: "/api"}, kind: "app"})
}

func TestRewritePath(t *testing.T) {
	cases := []struct{ req, route, rewrite, want string }{
		{"/api/orders", "/api", "/", "/orders"},
		{"/api", "/api", "/", "/"},
		{"/api/orders", "/api", "/v2/", "/v2/orders"},
		{"/orders", "/", "/v2", "/v2/orders"},
		{"/api/orders", "/api", "", "/api/orders"},
	}
	for _, c := range cases {
		if got := rewritePath(c.req, c.route, c.rewrite); got != c.want {
			t.Errorf("rewritePath(%q, %q, %q) = %q, want %q", c.req, c.route, c.rewrite, got, c.want)
		}
	}
}

func TestMiddlewarePathMatch(t *testing.T) {
	cases := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"/api/admin/x", []string{"/admin/*"}, true},
		{"/api/orders", []string{"/admin/*"}, false},
		{"/API/Admin", []string{"/admin"}, true},
		{"/api/orders", []string{"/.*"}, true},
		{"/x/admin", []string{"/admin"}, false},
	}
	for _, c := range cases {
		if got := middlewarePathMatch(c.path, "/api", c.patterns); got != c.want {
			t.Errorf("middlewarePathMatch(%q, %v) = %v, want %v", c.path, c.patterns, got, c.want)
		}
	}
}
