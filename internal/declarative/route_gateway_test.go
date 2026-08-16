// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import "testing"

func routeRes(name string, mutate func(*RouteSpec)) Resource {
	spec := &RouteSpec{Hosts: []string{"app.example.com"}, App: "web", Path: "/", TLS: "acme"}
	if mutate != nil {
		mutate(spec)
	}
	return Resource{APIVersion: APIVersion, Kind: KindRoute, Metadata: Meta{Name: name}, Route: spec}
}

func fieldNames(diffs []FieldDiff) []string {
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Field)
	}
	return out
}

// An omitted maintenance block and an explicitly disabled one describe the same
// route. If they compared unequal, every plan would report drift on a converged
// route and GitOps would rewrite it forever.
func TestOmittedMaintenanceIsNotDrift(t *testing.T) {
	live := routeRes("web", nil)
	desired := routeRes("web", func(s *RouteSpec) {
		s.Maintenance = &RouteMaintenanceSpec{Enabled: false}
	})
	if d := diffFields(live, desired); len(d) != 0 {
		t.Errorf("phantom drift: %v", fieldNames(d))
	}
}

func TestParkedRouteIsDriftAgainstAServingManifest(t *testing.T) {
	live := routeRes("web", func(s *RouteSpec) {
		s.Maintenance = &RouteMaintenanceSpec{Enabled: true, StatusCode: 503, Message: "brb"}
	})
	desired := routeRes("web", nil)
	got := fieldNames(diffFields(live, desired))
	if len(got) == 0 {
		t.Fatal("a manifest with no maintenance block did not plan to resume traffic")
	}
	want := map[string]bool{"maintenance.enabled": true, "maintenance.message": true, "maintenance.statusCode": true}
	for _, f := range got {
		if !want[f] {
			t.Errorf("unexpected drift field %q", f)
		}
	}
}

func TestMaintenanceMessageChangeIsDrift(t *testing.T) {
	live := routeRes("web", func(s *RouteSpec) {
		s.Maintenance = &RouteMaintenanceSpec{Enabled: true, Message: "back at 14:00"}
	})
	desired := routeRes("web", func(s *RouteSpec) {
		s.Maintenance = &RouteMaintenanceSpec{Enabled: true, Message: "back at 16:00"}
	})
	got := fieldNames(diffFields(live, desired))
	if len(got) != 1 || got[0] != "maintenance.message" {
		t.Errorf("fields = %v, want just maintenance.message", got)
	}
}

func TestEmptySecurityBlockIsNotDrift(t *testing.T) {
	live := routeRes("web", nil)
	desired := routeRes("web", func(s *RouteSpec) { s.Security = &RouteSecuritySpec{} })
	if d := diffFields(live, desired); len(d) != 0 {
		t.Errorf("phantom drift: %v", fieldNames(d))
	}
}

func TestExploitProtectionIsDiffed(t *testing.T) {
	live := routeRes("web", nil)
	desired := routeRes("web", func(s *RouteSpec) {
		s.Security = &RouteSecuritySpec{ExploitProtection: true}
	})
	got := fieldNames(diffFields(live, desired))
	if len(got) != 1 || got[0] != "security.exploitProtection" {
		t.Errorf("fields = %v, want just security.exploitProtection", got)
	}
	if d := diffFields(desired, desired); len(d) != 0 {
		t.Errorf("a converged protected route shows drift: %v", fieldNames(d))
	}
}

// Strict parsing: a typo must fail at apply time rather than being dropped.
func TestRouteRejectsUnknownGatewayKey(t *testing.T) {
	_, err := Parse([]byte(`apiVersion: miabi.io/v1
kind: Route
metadata:
  name: web
spec:
  hosts: [app.example.com]
  app: web
  security:
    exploitProtectionn: true
`))
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
}

func TestRouteParsesGatewayFields(t *testing.T) {
	rs, err := Parse([]byte(`apiVersion: miabi.io/v1
kind: Application
metadata:
  name: web
spec:
  image: nginx
---
apiVersion: miabi.io/v1
kind: Route
metadata:
  name: web
spec:
  hosts: [app.example.com]
  app: web
  security:
    exploitProtection: true
  maintenance:
    enabled: true
    statusCode: 503
    message: Back at 14:00 UTC
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var r Resource
	for _, res := range rs.All() {
		if res.Kind == KindRoute {
			r = res
		}
	}
	if r.Route == nil {
		t.Fatal("no Route parsed")
	}
	if r.Route.Security == nil || !r.Route.Security.ExploitProtection {
		t.Errorf("security = %+v", r.Route.Security)
	}
	if r.Route.Maintenance == nil || !r.Route.Maintenance.Enabled ||
		r.Route.Maintenance.StatusCode != 503 || r.Route.Maintenance.Message != "Back at 14:00 UTC" {
		t.Errorf("maintenance = %+v", r.Route.Maintenance)
	}
}
