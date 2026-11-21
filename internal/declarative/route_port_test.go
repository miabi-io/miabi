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

func routePort(ports, port string) string {
	return `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: nginx
  ports:
` + ports + `
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web }
spec:
  hosts: [shop.example.com]
  app: web
  port: ` + port + "\n"
}

const namedPorts = "    - { name: http, container: 8080 }\n    - { name: metrics, container: 9090 }"

func TestRoutePortByName(t *testing.T) {
	set, err := d.Parse([]byte(routePort(namedPorts, "metrics")))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := set.Get("Route/web")
	if r.Route.Port.Number != 9090 {
		t.Errorf("port = %+v, want 9090", r.Route.Port)
	}
}

// A named port and its number must plan alike, or naming the port would churn the route.
func TestRoutePortNameAndNumberPlanAlike(t *testing.T) {
	byName, err := d.Parse([]byte(routePort(namedPorts, "http")))
	if err != nil {
		t.Fatal(err)
	}
	byNumber, err := d.Parse([]byte(routePort(namedPorts, "8080")))
	if err != nil {
		t.Fatal(err)
	}
	if d.BuildPlan(byName, byNumber, d.PlanOptions{}).HasChanges() {
		t.Error("port: http planned a change against port: 8080")
	}
}

func TestRoutePortQuotedNumberIsANumber(t *testing.T) {
	set, err := d.Parse([]byte(routePort(namedPorts, `"8080"`)))
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := set.Get("Route/web"); r.Route.Port.Number != 8080 {
		t.Errorf("port = %+v", r.Route.Port)
	}
}

func TestPortNamesAreValidated(t *testing.T) {
	cases := []struct{ name, ports, port, want string }{
		{"unknown route port name", namedPorts, "grpc", `port "grpc" is not a port name of application "web"`},
		{"upper case name", "    - { name: HTTP, container: 8080 }", "8080", "must be lowercase"},
		{"digit-only name", "    - { name: \"8080\", container: 8080 }", "8080", "at least one letter"},
		{"name on two ports", "    - { name: http, container: 8080 }\n    - { name: http, container: 8081 }", "8080", "is used by ports 8080 and 8081"},
		{"port out of range", namedPorts, "70000", "out of range"},
		{"port as a block", namedPorts, "{ name: http }", "number or a port name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Parse([]byte(routePort(tc.ports, tc.port)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A DNS-style port serves tcp and udp under one name.
func TestOnePortNameForBothProtocols(t *testing.T) {
	ports := "    - { name: dns, container: 53, protocol: udp }\n    - { name: dns, container: 53, protocol: tcp }"
	if _, err := d.Parse([]byte(routePort(ports, "dns"))); err != nil {
		t.Error(err)
	}
}

// A name set in the console must survive a manifest that names no ports.
func TestPortNamesCompareOnlyWhenStated(t *testing.T) {
	live, err := d.Parse([]byte(routePort(namedPorts, "8080")))
	if err != nil {
		t.Fatal(err)
	}
	silent, err := d.Parse([]byte(routePort("    - { container: 8080 }\n    - { container: 9090 }", "8080")))
	if err != nil {
		t.Fatal(err)
	}
	if d.BuildPlan(silent, live, d.PlanOptions{}).HasChanges() {
		t.Error("a manifest without port names planned to clear them")
	}
	renamed, err := d.Parse([]byte(routePort("    - { name: web, container: 8080 }\n    - { name: metrics, container: 9090 }", "8080")))
	if err != nil {
		t.Fatal(err)
	}
	if !d.BuildPlan(renamed, live, d.PlanOptions{}).HasChanges() {
		t.Error("renaming a port planned no change")
	}
}

func appPorts(ports string) *d.ResourceSet {
	doc := `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: nginx
` + ports + "\n"
	set, err := d.Parse([]byte(doc))
	if err != nil {
		panic(err)
	}
	return set
}

// Adding, removing or changing a port must plan, or the manifest could never converge the app's ports.
func TestPortChangesPlan(t *testing.T) {
	live := appPorts("  ports: [{ container: 8080 }]")
	cases := map[string]string{
		"added":          "  ports: [{ container: 8080 }, { container: 9090 }]",
		"removed":        "  ports: [{ container: 9090 }]",
		"scheme changed": "  ports: [{ container: 8080, scheme: https }]",
		"protocol added": "  ports: [{ container: 8080 }, { container: 8080, protocol: udp }]",
	}
	for name, desired := range cases {
		if !d.BuildPlan(appPorts(desired), live, d.PlanOptions{}).HasChanges() {
			t.Errorf("%s: no change planned", name)
		}
	}
}

func TestPortsCompareOnlyWhenStated(t *testing.T) {
	live := appPorts("  ports: [{ container: 8080 }, { container: 9090 }]")
	if d.BuildPlan(appPorts(""), live, d.PlanOptions{}).HasChanges() {
		t.Error("a manifest without ports planned to change them")
	}
	if d.BuildPlan(appPorts("  ports: [{ container: 9090 }, { container: 8080, primary: true }]"), live, d.PlanOptions{}).HasChanges() {
		t.Error("reordering ports around the same primary planned a change")
	}
	if !d.BuildPlan(appPorts("  ports: [{ container: 9090 }, { container: 8080 }]"), live, d.PlanOptions{}).HasChanges() {
		t.Error("an unmarked reorder moves the primary to the first port and must plan")
	}
}

func TestPrimaryPortChangePlans(t *testing.T) {
	live := appPorts("  ports: [{ container: 8080 }, { container: 9090 }]")
	if !d.BuildPlan(appPorts("  ports: [{ container: 8080 }, { container: 9090, primary: true }]"), live, d.PlanOptions{}).HasChanges() {
		t.Error("moving the primary port planned nothing")
	}
}

func TestPrimaryPort(t *testing.T) {
	doc := func(ports string) string {
		return "apiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: web }\nspec:\n  image: nginx\n" + ports + "\n"
	}
	if _, err := d.Parse([]byte(doc("  ports: [{ container: 8080, primary: true }, { container: 9090, primary: true }]"))); err == nil {
		t.Error("two primary ports were accepted")
	}
	if _, err := d.Parse([]byte(doc("  ports: [{ container: 53, primary: true }, { container: 53, protocol: udp, primary: true }]"))); err != nil {
		t.Errorf("one port number over tcp and udp: %v", err)
	}
	app := func(ports string) *d.ApplicationSpec { return appPorts(ports).All()[0].Application }
	if p, _ := app("  ports: [{ container: 8080 }, { container: 9090, primary: true }]").PrimaryPort(); p.Container != 9090 {
		t.Errorf("marked primary = %d", p.Container)
	}
	if p, _ := app("  ports: [{ container: 8080 }, { container: 9090 }]").PrimaryPort(); p.Container != 8080 {
		t.Errorf("default primary = %d", p.Container)
	}
	if _, ok := app("").PrimaryPort(); ok {
		t.Error("an app without ports has a primary")
	}
}
