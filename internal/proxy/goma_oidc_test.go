// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// An OIDC policy has to reach Goma with its nested blocks intact: session and
// forward are objects, and a flattened or dropped level is a policy that does
// not do what the form said.
func TestRenderOIDCMiddlewareKeepsNestedBlocks(t *testing.T) {
	out, err := RenderMiddleware(RenderedMiddleware{
		ID: 7, WorkspaceID: 3, Name: "sso", Type: "oidc", Paths: []string{"/.*"},
		Rule: map[string]interface{}{
			"issuer":       "https://id.example.com/application/o/app/",
			"clientId":     "goma",
			"clientSecret": "s3cr3t",
			"scopes":       []interface{}{"openid", "email"},
			"callbackPath": "/oauth2/callback",
			"logoutPath":   "/oauth2/logout",
			"pkce":         true,
			"session": map[string]interface{}{
				"store": "redis", "ttl": "12h",
				"cookie": map[string]interface{}{"sameSite": "lax", "path": "/"},
			},
			"forward": map[string]interface{}{
				"headers":      map[string]interface{}{"X-Auth-Email": "email"},
				"stripInbound": true,
			},
			"claimsExpression": "Contains('groups', 'engineering')",
		},
	})
	if err != nil {
		t.Fatalf("RenderMiddleware: %v", err)
	}

	var file struct {
		Middlewares []struct {
			Name  string                 `yaml:"name"`
			Type  string                 `yaml:"type"`
			Paths []string               `yaml:"paths"`
			Rule  map[string]interface{} `yaml:"rule"`
		} `yaml:"middlewares"`
	}
	if err := yaml.Unmarshal(out, &file); err != nil {
		t.Fatalf("rendered middleware is not valid YAML: %v\n%s", err, out)
	}
	if len(file.Middlewares) != 1 {
		t.Fatalf("got %d middlewares, want 1:\n%s", len(file.Middlewares), out)
	}

	mw := file.Middlewares[0]
	if mw.Type != "oidc" {
		t.Errorf("type = %q, want oidc", mw.Type)
	}
	if !strings.HasSuffix(mw.Name, "-sso") {
		t.Errorf("name = %q, want the workspace-namespaced sso name", mw.Name)
	}

	session, ok := mw.Rule["session"].(map[string]interface{})
	if !ok {
		t.Fatalf("session did not survive as an object: %#v", mw.Rule["session"])
	}
	if session["store"] != "redis" || session["ttl"] != "12h" {
		t.Errorf("session = %#v, want the configured store and ttl", session)
	}
	cookie, ok := session["cookie"].(map[string]interface{})
	if !ok || cookie["sameSite"] != "lax" {
		t.Errorf("session.cookie = %#v, want the nested cookie block", session["cookie"])
	}

	forward, ok := mw.Rule["forward"].(map[string]interface{})
	if !ok {
		t.Fatalf("forward did not survive as an object: %#v", mw.Rule["forward"])
	}
	headers, ok := forward["headers"].(map[string]interface{})
	if !ok || headers["X-Auth-Email"] != "email" {
		t.Errorf("forward.headers = %#v, want the claim mapping", forward["headers"])
	}
	if forward["stripInbound"] != true {
		t.Error("forward.stripInbound was not carried through")
	}

	// A single-quoted expression must not be mangled by YAML quoting.
	if mw.Rule["claimsExpression"] != "Contains('groups', 'engineering')" {
		t.Errorf("claimsExpression = %#v", mw.Rule["claimsExpression"])
	}
}

// The registry's own gateway config is rendered by the same code path, so it is
// worth asserting it still comes out whole.
func TestRegistryConfigStillRenders(t *testing.T) {
	out, err := RenderMiddleware(RenderedMiddleware{
		ID: 1, WorkspaceID: 1, Name: "registry-auth", Type: "forwardAuth", Paths: []string{"/.*"},
		Rule: map[string]interface{}{
			"authUrl":             "http://miabi:9000/internal/registry/auth",
			"authResponseHeaders": []interface{}{"X-Miabi-Registry-Namespace"},
		},
	})
	if err != nil {
		t.Fatalf("RenderMiddleware: %v", err)
	}
	var file map[string]interface{}
	if err := yaml.Unmarshal(out, &file); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "authUrl") {
		t.Errorf("rendered middleware lost its rule:\n%s", out)
	}
}
