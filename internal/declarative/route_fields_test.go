// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

const routeWithRewrite = `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: api }
spec: { image: ghcr.io/org/api, ports: [{ container: 8080 }] }
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: api }
spec:
  hosts: [api.example.com]
  app: api
  path: /api
  rewrite: /
  methods: [GET, POST]
  advancedConfig: |
    cors:
      origins: ["https://app.example.com"]
`

func TestRouteRewriteMethodsAndAdvancedConfigParse(t *testing.T) {
	set, err := d.Parse([]byte(routeWithRewrite))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := set.Get("Route/api")
	if r.Route.Rewrite != "/" {
		t.Errorf("rewrite = %q", r.Route.Rewrite)
	}
	if strings.Join(r.Route.Methods, ",") != "GET,POST" {
		t.Errorf("methods = %v", r.Route.Methods)
	}
	if !strings.Contains(r.Route.AdvancedConfig, "cors:") {
		t.Errorf("advancedConfig = %q", r.Route.AdvancedConfig)
	}
}

// These fields were silently erased by every apply, because route.Update assigns them
// unconditionally and the manifest had no way to state them. Diffing them is what makes a change
// converge instead of vanishing.
func TestRouteFieldChangesConverge(t *testing.T) {
	mk := func(rewrite string) *d.ResourceSet {
		set, err := d.Parse([]byte(strings.Replace(routeWithRewrite, "rewrite: /", "rewrite: "+rewrite, 1)))
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	if !d.BuildPlan(mk("/v2"), mk("/"), d.PlanOptions{}).HasChanges() {
		t.Error("changing the rewrite did not plan a change")
	}
	if d.BuildPlan(mk("/"), mk("/"), d.PlanOptions{}).HasChanges() {
		t.Error("an unchanged route planned a change")
	}
}

// The gateway matches methods by membership, so listing them in another order is the same route and
// must not plan an update on every reconcile.
func TestMethodOrderIsNotDrift(t *testing.T) {
	mk := func(methods string) *d.ResourceSet {
		set, err := d.Parse([]byte(strings.Replace(routeWithRewrite, "[GET, POST]", methods, 1)))
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	if d.BuildPlan(mk("[POST, GET]"), mk("[GET, POST]"), d.PlanOptions{}).HasChanges() {
		t.Error("reordering methods planned a change")
	}
	if !d.BuildPlan(mk("[GET]"), mk("[GET, POST]"), d.PlanOptions{}).HasChanges() {
		t.Error("dropping a method did not plan a change")
	}
}

// A typo silently drops every request using the method the author meant — exactly the failure that
// only shows up in production.
func TestUnknownMethodIsRejected(t *testing.T) {
	_, err := d.Parse([]byte(strings.Replace(routeWithRewrite, "[GET, POST]", "[GET, FETCH]", 1)))
	if err == nil || !strings.Contains(err.Error(), "not an HTTP method") {
		t.Errorf("error = %v", err)
	}
}
