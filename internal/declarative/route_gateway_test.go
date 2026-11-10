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

package declarative

import "testing"

func routeRes(name string, mutate func(*RouteSpec)) Resource {
	spec := &RouteSpec{Hosts: []string{"app.example.com"}, App: "web", Path: "/"}
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

// Unequal comparison would make GitOps rewrite a converged route forever.
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

// An exported route must plan equal to the manifest that created it, or every GitOps sync would rewrite it.
func TestExportedTLSMatchesTheManifest(t *testing.T) {
	cases := []struct {
		mode, provider, cert string
		manifest             *RouteTLSSpec
	}{
		{"acme", "", "", nil},
		{"acme", "internal-ca", "", &RouteTLSSpec{Provider: "internal-ca"}},
		{"custom", "", "shop", &RouteTLSSpec{Certificate: "shop"}},
		{"off", "", "", &RouteTLSSpec{Enabled: new(false)}},
	}
	for _, tc := range cases {
		live := routeRes("web", func(s *RouteSpec) { s.TLS = RouteTLSOf(tc.mode, tc.provider, tc.cert) })
		desired := routeRes("web", func(s *RouteSpec) { s.TLS = tc.manifest })
		if d := diffFields(live, desired); len(d) != 0 {
			t.Errorf("%s: drift %v", tc.mode, fieldNames(d))
		}
	}
}

// A custom route whose certificate was deleted must still read as custom, so the plan shows the gap.
func TestExportedCustomWithoutCertificateStaysCustom(t *testing.T) {
	if got := (&RouteSpec{TLS: RouteTLSOf("custom", "", "")}).TLSMode(); got != "custom" {
		t.Errorf("mode = %q", got)
	}
}
