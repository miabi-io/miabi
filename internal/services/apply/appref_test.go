// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
)

func parseSet(t *testing.T, yaml string) *declarative.ResourceSet {
	t.Helper()
	set, err := declarative.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// The regression the id-derived alias could never pass: on a first apply neither app exists, and
// applications share a plan rank, so whichever is created first must still be able to address the
// other. It works because an app's address is its own name, known from the manifest alone.
func TestSiblingResolvesOnAFirstApplyWithNothingLive(t *testing.T) {
	set := parseSet(t, `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: ghcr.io/org/web
  ports: [{ container: 3000 }]
  env: { API_URL: "{{ .applications.api.url }}" }
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: api }
spec:
  image: ghcr.io/org/api
  ports: [{ container: 8080 }]
`)
	views := overlayDesiredApps(map[string]declarative.AppView{}, set)
	got, err := declarative.NewRenderer(declarative.RenderContext{Apps: views}).
		RenderString("t", "{{ .applications.api.url }}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://api:8080" {
		t.Errorf("url = %q", got)
	}
}

// An app already in the workspace but absent from the bundle is addressable too — you do not have to
// declare something you are not changing.
func TestLiveAppIsAddressableWithoutBeingDeclared(t *testing.T) {
	live := map[string]declarative.AppView{
		"api": {Host: "api", Port: "9000", Scheme: "http", Alias: "mb-app-x-4"},
	}
	views := overlayDesiredApps(live, parseSet(t, `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec: { image: ghcr.io/org/web, ports: [{ container: 3000 }] }
`))
	if got := views["api"].URL(); got != "http://api:9000" {
		t.Errorf("live app url = %q", got)
	}
}

// The bundle is the desired state, so a port change in the manifest wins over what is running.
func TestBundleWinsOverLiveState(t *testing.T) {
	live := map[string]declarative.AppView{"api": {Host: "api", Port: "9000", Scheme: "http", Alias: "mb-app-x-4"}}
	views := overlayDesiredApps(live, parseSet(t, `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: api }
spec: { image: ghcr.io/org/api, ports: [{ container: 8080, scheme: https }] }
`))
	if got := views["api"].URL(); got != "https://api:8080" {
		t.Errorf("url = %q, want the bundle's port and scheme", got)
	}
	// ...but the container identity is live state the manifest cannot restate, so it survives.
	if views["api"].Alias != "mb-app-x-4" {
		t.Errorf("alias = %q, want the live one preserved", views["api"].Alias)
	}
}

// A manifest that simply omits ports must not silently downgrade a running app's address to a
// portless URL.
func TestDeclaredWithoutPortsKeepsTheLivePort(t *testing.T) {
	live := map[string]declarative.AppView{"api": {Host: "api", Port: "9000", Scheme: "https"}}
	views := overlayDesiredApps(live, parseSet(t, `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: api }
spec: { image: ghcr.io/org/api }
`))
	if got := views["api"].URL(); got != "https://api:9000" {
		t.Errorf("url = %q", got)
	}
}

// Off cluster mode a workspace network is a node-local bridge, so this reference would render a
// hostname that never resolves. Failing the apply beats a connection refused hours later.
func TestCrossNodeReferenceIsRefused(t *testing.T) {
	nodes := map[string]appPlacement{
		"web": {id: 0, node: "the local node"},
		"api": {id: 3, node: "edge-1"},
	}
	err := crossNodeRefError("web", map[string]bool{"api": true}, nodes)
	if err == nil {
		t.Fatal("a cross-node reference was accepted")
	}
	for _, want := range []string{"web", "api", "the local node", "edge-1", "cluster mode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message is missing %q: %v", want, err)
		}
	}
}

func TestSameNodeReferenceIsFine(t *testing.T) {
	nodes := map[string]appPlacement{
		"web": {id: 3, node: "edge-1"},
		"api": {id: 3, node: "edge-1"},
	}
	if err := crossNodeRefError("web", map[string]bool{"api": true}, nodes); err != nil {
		t.Errorf("same-node reference refused: %v", err)
	}
}

// Placement the engine does not know is not placement it may reject: an app being created in this
// same apply has no node yet, and guessing would fail a manifest that is perfectly fine.
func TestUnknownPlacementIsNotRefused(t *testing.T) {
	nodes := map[string]appPlacement{"web": {id: 0, node: "the local node"}}
	if err := crossNodeRefError("web", map[string]bool{"api": true}, nodes); err != nil {
		t.Errorf("an unplaced target was refused: %v", err)
	}
	if err := crossNodeRefError("absent", map[string]bool{"api": true}, nodes); err != nil {
		t.Errorf("an unplaced referrer was refused: %v", err)
	}
}

// With two offending references the message must be the same on every run, or an operator re-running
// an apply sees a different cause each time.
func TestCrossNodeMessageIsDeterministic(t *testing.T) {
	nodes := map[string]appPlacement{
		"web": {id: 0, node: "local"}, "api": {id: 3, node: "edge-1"}, "cache": {id: 4, node: "edge-2"},
	}
	refs := map[string]bool{"api": true, "cache": true}
	first := crossNodeRefError("web", refs, nodes).Error()
	for i := 0; i < 30; i++ {
		if got := crossNodeRefError("web", refs, nodes).Error(); got != first {
			t.Fatalf("run %d reported a different reference:\n%s\n%s", i, got, first)
		}
	}
}

// The guard reads the RAW env, before rendering replaces the templates — so it has to recognise the
// forms people actually write.
func TestAppRefPatternFindsTheFormsPeopleWrite(t *testing.T) {
	for _, in := range []string{
		"{{ .applications.api }}",
		"{{ .applications.api.url }}",
		"{{.applications.api.host}}",
		"{{- .applications.api.port }}",
		"http://{{ .applications.api.host }}:8080/v1",
	} {
		m := appRefPattern.FindAllStringSubmatch(in, -1)
		if len(m) != 1 || m[0][1] != "api" {
			t.Errorf("%q did not yield the app name: %v", in, m)
		}
	}
	if m := appRefPattern.FindAllStringSubmatch("{{ .secrets.api }}", -1); len(m) != 0 {
		t.Errorf("a secret reference was read as an app reference: %v", m)
	}
}
