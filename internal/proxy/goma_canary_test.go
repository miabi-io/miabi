// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strings"
	"testing"
)

// The shape Goma documents for an attribute-matched canary. Rendering is pinned
// byte-for-byte so a change on either side breaks a test rather than a deployment.
const goldenCanaryRoute = `# Managed by Miabi. Do not edit by hand.
routes:
    - name: mb-ws1-web
      path: /
      hosts:
        - api.example.com
      backends:
        - endpoint: http://mb-app-1:8080
          weight: 80
        - endpoint: http://mb-app-1-canary:8080
          weight: 20
          exclusive: true
          priority: 5
          match:
            - source: header
              name: X-Canary-User
              operator: equals
              value: "true"
            - source: cookie
              name: beta_user
              operator: in
              value: admin,tester,developer
`

func TestRenderCanaryWithRulesMatchesGomaShape(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID: 1, WorkspaceID: 1, Name: "web", Hosts: []string{"api.example.com"},
		Backends: []Backend{
			{Endpoint: "http://mb-app-1:8080", Weight: 80},
			{
				Endpoint: "http://mb-app-1-canary:8080", Weight: 20, Exclusive: true, Priority: 5,
				Match: []MatchRule{
					{Source: "header", Name: "X-Canary-User", Operator: "equals", Value: "true"},
					{Source: "cookie", Name: "beta_user", Operator: "in", Value: "admin,tester,developer"},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != goldenCanaryRoute {
		t.Errorf("rendered canary route drifted from the documented Goma shape:\ngot:\n%s\nwant:\n%s", out, goldenCanaryRoute)
	}
}

// A plain weighted canary must render exactly as it did before match rules
// existed: no exclusive, priority or match keys anywhere.
func TestRenderCanaryWithoutRulesUnchanged(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID: 1, WorkspaceID: 1, Name: "web", Hosts: []string{"api.example.com"},
		Backends: []Backend{
			{Endpoint: "http://mb-app-1:8080", Weight: 80},
			{Endpoint: "http://mb-app-1-canary:8080", Weight: 20},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `# Managed by Miabi. Do not edit by hand.
routes:
    - name: mb-ws1-web
      path: /
      hosts:
        - api.example.com
      backends:
        - endpoint: http://mb-app-1:8080
          weight: 80
        - endpoint: http://mb-app-1-canary:8080
          weight: 20
`
	if string(out) != want {
		t.Errorf("plain canary route changed shape:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

// The advanced (raw YAML) route path builds its backends by hand, so it needs
// its own proof that the canary fields survive.
func TestRenderAdvancedRouteCarriesCanaryRules(t *testing.T) {
	out, err := RenderRoute(RenderedRoute{
		ID: 1, WorkspaceID: 1, Name: "web",
		AdvancedYAML: "path: /\nhosts:\n  - api.example.com\n",
		Backends: []Backend{
			{Endpoint: "http://mb-app-1:8080", Weight: 90},
			{
				Endpoint: "http://mb-app-1-canary:8080", Weight: 10, Exclusive: true, Priority: 3,
				Match: []MatchRule{{Source: "query", Name: "version", Operator: "equals", Value: "beta"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"exclusive: true", "priority: 3", "source: query", "name: version", "operator: equals", "value: beta"} {
		if !strings.Contains(s, want) {
			t.Errorf("advanced route missing %q:\n%s", want, s)
		}
	}
}
