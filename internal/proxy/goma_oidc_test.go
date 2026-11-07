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

package proxy

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A flattened or dropped nested level is a policy that does not do what the form said.
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
