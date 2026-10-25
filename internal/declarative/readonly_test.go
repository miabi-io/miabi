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

const readOnlyDB = `apiVersion: miabi.io/v1
kind: Database
metadata: { name: mail-db }
spec:
  engine: postgres
  version: "17-alpine"
  readOnlyUser: true
`

func TestReadOnlyUserParses(t *testing.T) {
	set, err := d.Parse([]byte(readOnlyDB))
	if err != nil {
		t.Fatal(err)
	}
	db := set.ByKind(d.KindDatabase)[0].Database
	if db.ReadOnlyUser == nil || !*db.ReadOnlyUser {
		t.Fatalf("readOnlyUser = %v", db.ReadOnlyUser)
	}
	if _, err := d.Parse([]byte(strings.Replace(readOnlyDB, "engine: postgres", "engine: mysql", 1))); err == nil {
		t.Error("readOnlyUser on mysql must be refused")
	}
}

func TestReadOnlyUserReferences(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{Databases: map[string]d.ConnView{
		"mail-db": {URI: "postgres://u:p@h/db", ReadOnlyUser: "r_1", ReadOnlyPassword: "rp", ReadOnlyURI: "postgres://r_1:rp@h/db"},
		"plain":   {URI: "postgres://u:p@h/other"},
	}})
	env, err := r.RenderEnv("app", map[string]string{
		"RO_USER": "{{ .databases.mail-db.readonly_user }}",
		"RO_PASS": "{{ .databases.mail-db.readonly_password }}",
		"RO_URI":  "{{ .databases.mail-db.readonly_uri }}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["RO_USER"] != "r_1" || env["RO_PASS"] != "rp" || env["RO_URI"] != "postgres://r_1:rp@h/db" {
		t.Fatalf("rendered %v", env)
	}
	_, err = r.RenderEnv("app", map[string]string{"RO": "{{ .databases.plain.readonly_uri }}"})
	if err == nil || !strings.Contains(err.Error(), "readOnlyUser: true") {
		t.Errorf("a database without a read-only user must say how to get one: %v", err)
	}
}

func TestReadOnlyUserPlan(t *testing.T) {
	spec := func(ro *bool) *d.ResourceSet {
		set := d.NewResourceSet()
		set.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindDatabase, Metadata: d.Meta{Name: "mail-db"},
			Database: &d.DatabaseSpec{Engine: "postgres", Version: "17-alpine", Instance: "auto", ReadOnlyUser: ro}})
		return set
	}
	on, off := true, false
	updates := func(desired, actual *d.ResourceSet) int {
		_, upd, _, _ := d.BuildPlan(desired, actual, d.PlanOptions{}).Counts()
		return upd
	}
	if n := updates(spec(nil), spec(&on)); n != 0 {
		t.Errorf("a manifest silent about readOnlyUser must keep it: %d updates", n)
	}
	if n := updates(spec(&on), spec(&off)); n != 1 {
		t.Errorf("enabling it is an update: %d", n)
	}
	if n := updates(spec(&off), spec(&on)); n != 1 {
		t.Errorf("disabling it is an update: %d", n)
	}
	if n := updates(spec(&on), spec(&on)); n != 0 {
		t.Errorf("unchanged: %d updates", n)
	}
}
