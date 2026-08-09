// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import "testing"

const ghostYAML = `
apiVersion: miabi.io/v1
kind: Template
metadata:
  name: ghost
  displayName: Ghost
  version: 1.2.0
  category: CMS
inputs:
  - key: site_url
    type: string
    required: true
databases:
  - name: db
    engine: mysql
volumes:
  - name: content
applications:
  - name: ghost
    image: ghost
    tag: "5-alpine"
    ports:
      - container: 2368
    env:
      url: "{{ .inputs.site_url }}"
      database__connection__host: "{{ .databases.db.host }}"
      database__connection__password: "{{ .databases.db.password }}"
    secretEnv: [database__connection__password]
    mounts:
      - volume: content
        path: /var/lib/ghost/content
    resources:
      memory: 512Mi
      cpu: 0.5
`

func TestParseGhost(t *testing.T) {
	m, err := Parse([]byte(ghostYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Metadata.Name != "ghost" {
		t.Errorf("name = %q", m.Metadata.Name)
	}
	if m.Metadata.DisplayName != "Ghost" {
		t.Errorf("displayName = %q", m.Metadata.DisplayName)
	}
	if m.Databases[0].Placement != PlacementAuto {
		t.Errorf("placement default = %q, want auto", m.Databases[0].Placement)
	}
	if m.Applications[0].Ports[0].Scheme != "http" {
		t.Errorf("port scheme default = %q, want http", m.Applications[0].Ports[0].Scheme)
	}
	if !m.Applications[0].Primary {
		t.Error("single service should be implicitly primary")
	}
	mem, _ := m.Applications[0].Resources.MemoryBytes()
	if mem != 512*1024*1024 {
		t.Errorf("memory = %d, want %d", mem, 512*1024*1024)
	}
	cpu, _ := m.Applications[0].Resources.NanoCPUs()
	if cpu != 500_000_000 {
		t.Errorf("cpu = %d, want 5e8", cpu)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	y := ghostYAML + "\nbogusTopLevel: true\n"
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestValidateRedisNotShared(t *testing.T) {
	y := `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: r, displayName: R, version: "1.0.0"}
databases:
  - name: cache
    engine: redis
    placement: shared
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatal("expected error: redis cannot be shared")
	}
}

func TestValidateMountUnknownVolume(t *testing.T) {
	y := `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: a, displayName: A, version: "1.0.0"}
applications:
  - name: app
    image: nginx
    mounts:
      - volume: missing
        path: /data
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatal("expected error: mount references unknown volume")
	}
}

const stackYAML = `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: a, displayName: A, version: "1.0.0"}
databases:
  - name: db
    engine: postgres
stack:
  description: A grouped install.
  env:
    DB_HOST: "{{ .databases.db.host }}"
    DB_PASSWORD: "{{ .databases.db.password }}"
  secretEnv: [DB_PASSWORD]
  annotations:
    docs: https://example.com
applications:
  - name: app
    image: nginx
`

func TestParseStackBlock(t *testing.T) {
	m, err := Parse([]byte(stackYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Stack == nil {
		t.Fatal("stack block should be parsed")
	}
	if m.Stack.Env["DB_HOST"] == "" || m.Stack.SecretEnv[0] != "DB_PASSWORD" {
		t.Errorf("stack env/secretEnv not parsed: %+v", m.Stack)
	}
	if m.Stack.Annotations["docs"] != "https://example.com" {
		t.Errorf("stack annotations not parsed: %+v", m.Stack.Annotations)
	}
	// A stack block forces a stack even though there is a single application.
	if !m.WantsStack() {
		t.Error("a declared stack block should force a stack for a single app")
	}
}

func TestWantsStack(t *testing.T) {
	multi := `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: a, displayName: A, version: "1.0.0"}
applications:
  - name: a
    image: nginx
  - name: b
    image: nginx
`
	m, err := Parse([]byte(multi))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !m.WantsStack() {
		t.Error("a multi-application template should want a stack")
	}

	single := `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: a, displayName: A, version: "1.0.0"}
applications:
  - name: a
    image: nginx
`
	m, err = Parse([]byte(single))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.WantsStack() {
		t.Error("a single-application template without a stack block should not want a stack")
	}
}

func TestValidateStackSecretEnvMustBeDeclared(t *testing.T) {
	y := `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: a, displayName: A, version: "1.0.0"}
stack:
  secretEnv: [MISSING]
applications:
  - name: app
    image: nginx
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatal("expected error: stack secretEnv not declared in stack env")
	}
}

func TestValidateStackRequiresApplications(t *testing.T) {
	y := `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: a, displayName: A, version: "1.0.0"}
databases:
  - name: db
    engine: postgres
stack:
  description: orphan
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatal("expected error: a database-only template cannot declare a stack")
	}
}

func TestRenderResolvesContext(t *testing.T) {
	m, err := Parse([]byte(ghostYAML))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRenderer(Context{
		Inputs:    map[string]string{"site_url": "https://blog.example.com"},
		Databases: map[string]ConnView{"db": {Host: "mb-db-1", Password: "s3cret"}},
	})
	env, err := r.RenderEnv("ghost", m.Applications[0].Env)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if env["url"] != "https://blog.example.com" {
		t.Errorf("url = %q", env["url"])
	}
	if env["database__connection__host"] != "mb-db-1" {
		t.Errorf("host = %q", env["database__connection__host"])
	}
	if env["database__connection__password"] != "s3cret" {
		t.Errorf("password = %q", env["database__connection__password"])
	}
}

// Templates spell the connection string either way; both must resolve to the URI.
func TestRenderDatabaseURLAliasesURI(t *testing.T) {
	r := NewRenderer(Context{
		Databases: map[string]ConnView{"db": {URI: "postgres://u:p@mb-db-1:5432/app"}},
	})
	for _, ref := range []string{"{{ .databases.db.uri }}", "{{ .databases.db.url }}"} {
		got, err := r.RenderString("x", ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got != "postgres://u:p@mb-db-1:5432/app" {
			t.Errorf("%s = %q", ref, got)
		}
	}
}

func TestRenderMissingKeyIsError(t *testing.T) {
	r := NewRenderer(Context{})
	if _, err := r.RenderString("x", "{{ .inputs.nope }}"); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestRandAlphaNumLength(t *testing.T) {
	if got := len(RandAlphaNum(24)); got != 24 {
		t.Errorf("len = %d, want 24", got)
	}
}
