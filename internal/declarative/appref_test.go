// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

func apiView() map[string]d.AppView {
	return map[string]d.AppView{
		"api": {Host: "api", Port: "8080", Scheme: "http", Alias: "mb-app-eqi3tlf2-11"},
	}
}

// The whole point: an author writes the name they gave the app, not the alias the platform minted.
func TestApplicationReferenceFields(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{Apps: apiView()})
	cases := map[string]string{
		"{{ .applications.api.host }}":   "api",
		"{{ .applications.api.port }}":   "8080",
		"{{ .applications.api.scheme }}": "http",
		"{{ .applications.api.url }}":    "http://api:8080",
		// A bare reference is the address to dial, mirroring how a bare .databases.x is its URI.
		"{{ .applications.api }}": "http://api:8080",
		// The container's exact identity still resolves: Config files already use it.
		"{{ .applications.api.alias }}": "mb-app-eqi3tlf2-11",
	}
	for in, want := range cases {
		got, err := r.RenderString("t", in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", in, got, want)
		}
	}
}

func TestApplicationURLHonoursTheScheme(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{Apps: map[string]d.AppView{
		"api": {Host: "api", Port: "8443", Scheme: "https"},
	}})
	got, _ := r.RenderString("t", "{{ .applications.api }}")
	if got != "https://api:8443" {
		t.Errorf("url = %q", got)
	}
}

// An app that declares no port has no ":" to append — a trailing colon would be a worse address
// than a bare hostname.
func TestApplicationURLWithoutAPort(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{Apps: map[string]d.AppView{"api": {Host: "api"}}})
	got, _ := r.RenderString("t", "{{ .applications.api }}")
	if got != "http://api" {
		t.Errorf("url = %q, want no trailing colon", got)
	}
}

// Unresolvable references are hard errors everywhere else in the grammar, and must stay so here:
// a silently empty API_URL is a runtime mystery.
func TestUnknownApplicationIsAnError(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{Apps: apiView()})
	if _, err := r.RenderString("t", "{{ .applications.nope.host }}"); err == nil {
		t.Error("an unknown application resolved")
	}
	if _, err := r.RenderString("t", "{{ .applications.api.nope }}"); err == nil {
		t.Error("an unknown field resolved")
	}
}

// Hyphenated names go through the ref() rewrite, which the dot-notation path cannot express.
func TestHyphenatedApplicationReference(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{Apps: map[string]d.AppView{
		"shop-api": {Host: "shop-api", Port: "3000", Scheme: "http"},
	}})
	got, err := r.RenderString("t", "{{ .applications.shop-api.url }}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://shop-api:3000" {
		t.Errorf("url = %q", got)
	}
}

// The edge already existed; this pins that it still fires on the field forms people will write.
func TestAppRefEdgesFromEnv(t *testing.T) {
	set, err := d.Parse([]byte(`
apiVersion: miabi.io/v1
kind: Application
metadata: { name: api }
spec: { image: ghcr.io/org/api, ports: [{ container: 8080 }] }
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: ghcr.io/org/web
  ports: [{ container: 3000 }]
  env:
    API_URL: "{{ .applications.api.url }}"
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range d.Edges(set) {
		if e.Type == d.EdgeAppRef && e.From == "Application/web" && e.To == "Application/api" {
			return
		}
	}
	t.Error("no app-ref edge from web to api")
}
