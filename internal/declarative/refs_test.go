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
	"sort"
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

const refBundle = `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: ghcr.io/org/web
  env:
    DATABASE_URL: "{{ .databases.shop-db.uri }}"
    API_KEY: "{{ .secrets.api-key }}"
    VAULT: "${{ secrets.runtime-only }}"
    LITERAL: "costs .secrets.nothing outside an action"
---
apiVersion: miabi.io/v1
kind: Registry
metadata: { name: ghcr }
spec:
  server: ghcr.io
  username: bot
  password: "{{ .secrets.ghcr-token }}"
---
apiVersion: miabi.io/v1
kind: Middleware
metadata: { name: auth }
spec:
  type: basicAuth
  rule:
    users:
      - "admin:{{ .secrets.admin-hash }}"
---
apiVersion: miabi.io/v1
kind: Config
metadata: { name: app-conf }
spec:
  delimiters: ["<<", ">>"]
  data:
    app.ini: |
      api = << .applications.api.url >>
      untouched = {{ .secrets.not-a-ref-here }}
`

func refStrings(refs []d.Reference) []string {
	var out []string
	for _, r := range refs {
		s := "." + r.Collection + "." + r.Name
		if r.Field != "" {
			s += "." + r.Field
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// References come from every value apply renders, and only from inside its delimiters.
func TestReferencesFound(t *testing.T) {
	set, err := d.Parse([]byte(refBundle))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(refStrings(set.References()), " ")
	want := ".applications.api.url .databases.shop-db.uri .secrets.admin-hash .secrets.api-key .secrets.ghcr-token .secrets.runtime-only"
	if got != want {
		t.Errorf("references = %s\nwant          %s", got, want)
	}
}

func checkBundle(t *testing.T, bundle string, known d.KnownNames) error {
	t.Helper()
	set, err := d.Parse([]byte(bundle))
	if err != nil {
		t.Fatal(err)
	}
	return d.CheckReferences(set.References(), known)
}

func envApp(env string) string {
	return `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: ghcr.io/org/web
  env:
` + env
}

func known(dbs, secrets, apps []string) d.KnownNames {
	k := d.KnownNames{"databases": {}, "secrets": {}, "applications": {}, "inputs": {}}
	for _, n := range dbs {
		k["databases"][n] = true
	}
	for _, n := range secrets {
		k["secrets"][n] = true
	}
	for _, n := range apps {
		k["applications"][n] = true
	}
	return k
}

// #451: a typo'd secret only failed at apply, after the dry run said everything was fine.
func TestCheckReferencesFlagsTypo(t *testing.T) {
	err := checkBundle(t, envApp(`    DB_PASS: "{{ .secrets.db-pasword }}"`+"\n"), known(nil, []string{"db-password"}, nil))
	if err == nil || !strings.Contains(err.Error(), `unknown secret "db-pasword"`) || !strings.Contains(err.Error(), `application "web" env DB_PASS`) {
		t.Errorf("error = %v, want the unknown secret and where it is used", err)
	}
}

// Env resolves the runtime form at apply too, so its typo must fail the dry run like the template form's.
func TestCheckReferencesFlagsRuntimeSecretTypo(t *testing.T) {
	env := `    DB_PASS: "postgres://app:${{ secrets.db-pasword }}@db/app"
    OK: "${{secrets.db-password}}"
`
	err := checkBundle(t, envApp(env), known(nil, []string{"db-password"}, nil))
	if err == nil || !strings.Contains(err.Error(), `unknown secret "db-pasword"`) || !strings.Contains(err.Error(), `application "web" env DB_PASS`) {
		t.Errorf("error = %v, want the unknown secret and where it is used", err)
	}
	if strings.Contains(err.Error(), "env OK") {
		t.Errorf("a known runtime reference was flagged: %v", err)
	}
}

func TestCheckReferencesAcceptsKnownNames(t *testing.T) {
	env := `    A: "{{ .databases.shop-db.host }}"
    B: "{{ .databases.shop-db }}"
    C: "{{ .secrets.key }}"
    D: "{{ .applications.api.url }}"
`
	if err := checkBundle(t, envApp(env), known([]string{"shop-db"}, []string{"key"}, []string{"api"})); err != nil {
		t.Errorf("known references were refused: %v", err)
	}
}

func TestCheckReferencesFlagsBadField(t *testing.T) {
	err := checkBundle(t, envApp(`    A: "{{ .databases.shop-db.uri2 }}"`+"\n"), known([]string{"shop-db"}, nil, nil))
	if err == nil || !strings.Contains(err.Error(), `database "shop-db" has no field "uri2"`) {
		t.Errorf("error = %v, want the bad field named", err)
	}
}

// Inputs only exist in marketplace templates, so an apply can never resolve one.
func TestCheckReferencesFlagsInputs(t *testing.T) {
	err := checkBundle(t, envApp(`    A: "{{ .inputs.domain }}"`+"\n"), known(nil, nil, nil))
	if err == nil || !strings.Contains(err.Error(), `unknown input "domain"`) {
		t.Errorf("error = %v, want the input flagged", err)
	}
}

// One typo used twice is reported once per place, and every problem is listed, not just the first.
func TestCheckReferencesListsEveryProblem(t *testing.T) {
	env := `    A: "{{ .secrets.missing }}"
    B: "{{ .secrets.missing }}"
    C: "{{ .databases.gone }}"
`
	err := checkBundle(t, envApp(env), known(nil, nil, nil))
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := strings.Count(err.Error(), `unknown secret "missing"`); n != 2 {
		t.Errorf("secret reported %d times, want once per env key (2): %v", n, err)
	}
	if !strings.Contains(err.Error(), `unknown database "gone"`) {
		t.Errorf("the second problem is missing: %v", err)
	}
}
