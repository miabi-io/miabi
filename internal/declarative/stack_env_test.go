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

func stackWithEnv(env string) string {
	return `
apiVersion: miabi.io/v1
kind: Stack
metadata: { name: shop }
spec:
  description: Shop
` + env
}

func stackEnvDiffs(t *testing.T, desired, actual string) map[string]d.FieldDiff {
	t.Helper()
	ds, err := d.Parse([]byte(desired))
	if err != nil {
		t.Fatal(err)
	}
	as, err := d.Parse([]byte(actual))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]d.FieldDiff{}
	for _, c := range d.BuildPlan(ds, as, d.PlanOptions{}).Changes {
		for _, f := range c.Fields {
			if strings.HasPrefix(f.Field, "env.") {
				out[f.Field] = f
			}
		}
	}
	return out
}

func TestStackEnvParses(t *testing.T) {
	set, err := d.Parse([]byte(stackWithEnv(`  env:
    DB_HOST: "{{ .databases.shop-db.host }}"
    API_KEY: "{{ .secrets.api-key }}"
  secretEnv: [API_KEY]
`)))
	if err != nil {
		t.Fatal(err)
	}
	st, ok := set.Get(string(d.KindStack) + "/shop")
	if !ok || st.Stack == nil {
		t.Fatal("stack not parsed")
	}
	if len(st.Stack.Env) != 2 || len(st.Stack.SecretEnv) != 1 {
		t.Errorf("env = %v, secretEnv = %v", st.Stack.Env, st.Stack.SecretEnv)
	}
}

func TestStackSecretEnvMustBeDeclared(t *testing.T) {
	_, err := d.Parse([]byte(stackWithEnv(`  env: { A: "1" }
  secretEnv: [B]
`)))
	if err == nil || !strings.Contains(err.Error(), `secretEnv "B" is not declared in env`) {
		t.Errorf("err = %v, want an undeclared secretEnv error", err)
	}
}

// Stack env is rendered by apply, so its references are checked like an application's.
func TestStackEnvReferencesAreListed(t *testing.T) {
	set, err := d.Parse([]byte(stackWithEnv(`  env: { DB_HOST: "{{ .databases.shop-db.host }}" }
`)))
	if err != nil {
		t.Fatal(err)
	}
	refs := set.References()
	if len(refs) != 1 || refs[0].Collection != "databases" || refs[0].Name != "shop-db" {
		t.Errorf("references = %+v, want the shop-db reference", refs)
	}
}

func TestStatedStackEnvConverges(t *testing.T) {
	diffs := stackEnvDiffs(t,
		stackWithEnv("  env: { A: \"2\", B: \"new\" }\n"),
		stackWithEnv("  env: { A: \"1\", C: \"gone\" }\n"))
	if f := diffs["env.A"]; f.From != "1" || f.To != "2" {
		t.Errorf("env.A = %+v, want 1 -> 2", f)
	}
	if f := diffs["env.B"]; f.To != "new" {
		t.Errorf("env.B = %+v, want added", f)
	}
	if f, ok := diffs["env.C"]; !ok || f.To != "" {
		t.Errorf("env.C = %+v, want removed", f)
	}
}

// A manifest silent about env leaves shared variables set in the console alone.
func TestOmittedStackEnvIsNotDrift(t *testing.T) {
	if diffs := stackEnvDiffs(t, stackWithEnv(""), stackWithEnv("  env: { A: \"1\" }\n")); len(diffs) > 0 {
		t.Errorf("a stack without env planned %+v", diffs)
	}
}

// Stated empty, env removes every shared variable.
func TestEmptyStackEnvClears(t *testing.T) {
	diffs := stackEnvDiffs(t, stackWithEnv("  env: {}\n"), stackWithEnv("  env: { A: \"1\" }\n"))
	if f, ok := diffs["env.A"]; !ok || f.To != "" {
		t.Errorf("env.A = %+v, want removed", f)
	}
}

func TestStackSecretEnvIsMasked(t *testing.T) {
	diffs := stackEnvDiffs(t,
		stackWithEnv("  env: { K: \"plaintext\" }\n  secretEnv: [K]\n"),
		stackWithEnv("  env: { K: \"old\" }\n"))
	f := diffs["env.K"]
	if f.To != "(secret)" {
		t.Errorf("env.K = %+v, want the value masked", f)
	}
}
