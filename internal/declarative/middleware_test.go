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

package declarative_test

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

const middlewareYAML = `
apiVersion: miabi.io/v1
kind: Middleware
metadata: { name: api-ratelimit }
spec:
  type: rateLimit
  paths: ["/api"]
  rule:
    unit: minute
    requestsPerUnit: 60
---
apiVersion: miabi.io/v1
kind: Middleware
metadata: { name: admin-auth }
spec:
  type: basicAuth
  rule:
    realm: Admin
    users:
      - username: ops
        password: "{{ .secrets.ops_password }}"
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: ghcr.io/org/web
  ports: [{ container: 8080 }]
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web }
spec:
  hosts: [app.example.com]
  app: web
  middlewares: [api-ratelimit, admin-auth]
`

func TestParseMiddlewareAndRouteChain(t *testing.T) {
	set, err := d.Parse([]byte(middlewareYAML))
	if err != nil {
		t.Fatal(err)
	}
	mw, ok := set.Get("Middleware/api-ratelimit")
	if !ok {
		t.Fatal("middleware not parsed")
	}
	if mw.Middleware.Type != "rateLimit" {
		t.Errorf("type = %q", mw.Middleware.Type)
	}
	if got := mw.Middleware.Rule["requestsPerUnit"]; got != 60 {
		t.Errorf("rule value = %v (%T), want the number as authored", got, got)
	}
	rt, ok := set.Get("Route/web")
	if !ok {
		t.Fatal("route not parsed")
	}
	if strings.Join(rt.Route.Middlewares, ",") != "api-ratelimit,admin-auth" {
		t.Errorf("chain = %v, want the order as written", rt.Route.Middlewares)
	}
}

// The gateway runs the chain in order, so swapping entries must plan as an update.
func TestRouteChainOrderIsAChange(t *testing.T) {
	base := `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec: { image: ghcr.io/org/web, ports: [{ container: 8080 }] }
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web }
spec: { hosts: [a.example.com], app: web, middlewares: [%s] }
`
	actual, err := d.Parse([]byte(strings.Replace(base, "%s", "rate-limit, basic-auth", 1)))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := d.Parse([]byte(strings.Replace(base, "%s", "basic-auth, rate-limit", 1)))
	if err != nil {
		t.Fatal(err)
	}
	plan := d.BuildPlan(desired, actual, d.PlanOptions{})
	for _, c := range plan.Changes {
		if c.Kind == d.KindRoute && c.Action == d.ActionUpdate {
			return
		}
	}
	t.Errorf("reordering the chain did not plan an update: %+v", plan.Changes)
}

// An unchanged chain must be in sync, or every plan shows drift on a converged route.
func TestUnchangedRouteChainIsInSync(t *testing.T) {
	y := `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec: { image: ghcr.io/org/web, ports: [{ container: 8080 }] }
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web }
spec: { hosts: [a.example.com], app: web, middlewares: [rate-limit] }
`
	a, err := d.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if d.BuildPlan(b, a, d.PlanOptions{}).HasChanges() {
		t.Error("a converged route with a chain planned changes")
	}
}

// A middleware must exist before a route naming it, or the apply fails.
func TestMiddlewaresApplyBeforeRoutes(t *testing.T) {
	set, err := d.Parse([]byte(middlewareYAML))
	if err != nil {
		t.Fatal(err)
	}
	plan := d.BuildPlan(set, d.NewResourceSet(), d.PlanOptions{})
	mwAt, routeAt := -1, -1
	for i, c := range plan.Changes {
		switch c.Kind {
		case d.KindMiddleware:
			if mwAt < 0 || i > mwAt {
				mwAt = i
			}
		case d.KindRoute:
			routeAt = i
		}
	}
	if mwAt < 0 || routeAt < 0 {
		t.Fatalf("plan is missing a kind: %+v", plan.Changes)
	}
	if mwAt > routeAt {
		t.Errorf("the last middleware is created at %d, after the route at %d", mwAt, routeAt)
	}
}

// Without fingerprints on both sides a plan must not invent drift.
func TestMiddlewareRuleDiffNeedsFingerprintsOnBothSides(t *testing.T) {
	mk := func(fp string) *d.ResourceSet {
		set := d.NewResourceSet()
		set.Add(d.Resource{
			APIVersion: d.APIVersion, Kind: d.KindMiddleware,
			Metadata:   d.Meta{Name: "auth"},
			Middleware: &d.MiddlewareSpec{Type: "basicAuth", RuleFP: fp},
		})
		return set
	}
	if d.BuildPlan(mk("abc"), mk(""), d.PlanOptions{}).HasChanges() {
		t.Error("an unstamped actual side planned a change")
	}
	if !d.BuildPlan(mk("def"), mk("abc"), d.PlanOptions{}).HasChanges() {
		t.Error("a rotated rule did not plan a change")
	}
	if d.BuildPlan(mk("abc"), mk("abc"), d.PlanOptions{}).HasChanges() {
		t.Error("an unchanged rule planned a change")
	}
}

func TestMiddlewareValidation(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"type required", `
apiVersion: miabi.io/v1
kind: Middleware
metadata: { name: m }
spec: { rule: { a: 1 } }`, "type is required"},
		{"empty path", `
apiVersion: miabi.io/v1
kind: Middleware
metadata: { name: m }
spec: { type: rateLimit, paths: ["", "/api"] }`, "empty entry"},
		{"duplicate in chain", `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec: { image: x, ports: [{ container: 80 }] }
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web }
spec: { hosts: [a.example.com], app: web, middlewares: [auth, auth] }`, "listed twice"},
		{"empty name in chain", `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec: { image: x, ports: [{ container: 80 }] }
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web }
spec: { hosts: [a.example.com], app: web, middlewares: [""] }`, "empty name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// An undeclared middleware name may be a workspace default, so it is not a dangling reference.
func TestRouteMayNameAnUndeclaredMiddleware(t *testing.T) {
	_, err := d.Parse([]byte(`
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec: { image: x, ports: [{ container: 80 }] }
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web }
spec: { hosts: [a.example.com], app: web, middlewares: [seeded-basic-auth] }`))
	if err != nil {
		t.Errorf("a route naming a workspace middleware was refused: %v", err)
	}
}

func TestEdgesLinkRoutesToDeclaredMiddlewares(t *testing.T) {
	set, err := d.Parse([]byte(middlewareYAML))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range d.Edges(set) {
		if e.Type == d.EdgeMiddleware {
			got = append(got, e.From+"->"+e.To)
		}
	}
	if len(got) != 2 {
		t.Errorf("middleware edges = %v, want one per declared middleware", got)
	}
}
