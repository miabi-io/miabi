// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func renderMap(t *testing.T, r RenderedRoute) map[string]any {
	t.Helper()
	out, err := RenderRoute(r)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var f struct {
		Routes []map[string]any `yaml:"routes"`
	}
	if err := yaml.Unmarshal(out, &f); err != nil {
		t.Fatalf("parse rendered yaml: %v\n%s", err, out)
	}
	if len(f.Routes) != 1 {
		t.Fatalf("want 1 route, got %d:\n%s", len(f.Routes), out)
	}
	return f.Routes[0]
}

func base(name string) RenderedRoute {
	return RenderedRoute{
		ID: 1, Name: name, Hosts: []string{"app.example.com"},
		Backends: []Backend{{Endpoint: "http://mb-app-1:80"}},
	}
}

func TestExploitProtectionRendersOnlyWhenOn(t *testing.T) {
	r := base("guarded")
	r.ExploitProtection = true
	sec, ok := renderMap(t, r)["security"].(map[string]any)
	if !ok {
		t.Fatal("no security block for a protected route")
	}
	if v, _ := sec["enableExploitProtection"].(bool); !v {
		t.Error("enableExploitProtection was not rendered")
	}
}

func TestMaintenanceRendersWithGatewayDefaults(t *testing.T) {
	r := base("parked")
	r.Maintenance = &RouteMaintenance{Enabled: true}
	mt, ok := renderMap(t, r)["maintenance"].(map[string]any)
	if !ok {
		t.Fatal("no maintenance block")
	}
	if v, _ := mt["enabled"].(bool); !v {
		t.Error("maintenance.enabled was not rendered")
	}
	for _, k := range []string{"statusCode", "message"} {
		if _, ok := mt[k]; ok {
			t.Errorf("%s should be omitted when unset, got %v", k, mt[k])
		}
	}
}

func TestMaintenanceCarriesStatusAndMessage(t *testing.T) {
	r := base("parked")
	r.Maintenance = &RouteMaintenance{Enabled: true, StatusCode: 418, Message: "back at 14:00 UTC"}
	mt := renderMap(t, r)["maintenance"].(map[string]any)
	if mt["statusCode"] != 418 || mt["message"] != "back at 14:00 UTC" {
		t.Errorf("maintenance = %v", mt)
	}
}

func TestMaintenanceDisabledOmitsBlock(t *testing.T) {
	r := base("live")
	r.Maintenance = &RouteMaintenance{Enabled: false, Message: "ignored"}
	if _, ok := renderMap(t, r)["maintenance"]; ok {
		t.Error("a live route emitted a maintenance block")
	}
}

func TestExploitProtectionCoexistsWithTLSSkip(t *testing.T) {
	r := base("mixed")
	r.Backends = []Backend{{Endpoint: "https://mb-app-1:8443"}}
	r.ExploitProtection = true

	sec := renderMap(t, r)["security"].(map[string]any)
	tls, ok := sec["tls"].(map[string]any)
	if !ok || tls["insecureSkipVerify"] != true {
		t.Errorf("lost the TLS skip: %v", sec)
	}
	if sec["enableExploitProtection"] != true {
		t.Errorf("switches missing: %v", sec)
	}
}

func TestAdvancedSecurityIsMergedNotReplaced(t *testing.T) {
	r := base("adv")
	r.AdvancedYAML = "path: /\nsecurity:\n  tls:\n    rootCAs: /etc/ca.pem\n  enableExploitProtection: false\n"
	r.ExploitProtection = true

	sec := renderMap(t, r)["security"].(map[string]any)
	if v, _ := sec["enableExploitProtection"].(bool); !v {
		t.Error("Miabi's switch did not win over the hand-typed value")
	}
	tls, ok := sec["tls"].(map[string]any)
	if !ok || tls["rootCAs"] != "/etc/ca.pem" {
		t.Errorf("hand-authored rootCAs was dropped: %v", sec)
	}
}

func TestAdvancedMaintenanceIsInjected(t *testing.T) {
	r := base("adv")
	r.AdvancedYAML = "path: /\n"
	r.Maintenance = &RouteMaintenance{Enabled: true, StatusCode: 503}

	out, err := RenderRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "maintenance:") {
		t.Errorf("maintenance missing from an advanced route:\n%s", out)
	}
}

func TestRenderedChainKeepsAuthoredOrder(t *testing.T) {
	r := base("ordered")
	r.WorkspaceID = 2
	r.Middlewares = []string{"rate-limit", "basic-auth", "cors"}

	got, _ := renderMap(t, r)["middlewares"].([]any)
	want := []string{"mb-ws2-rate-limit", "mb-ws2-basic-auth", "mb-ws2-cors"}
	if len(got) != len(want) {
		t.Fatalf("middlewares = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chain order changed at %d: got %v, want %v", i, got, want)
		}
	}
}
