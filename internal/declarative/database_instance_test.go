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

const sharedPostgres = `apiVersion: miabi.io/v1
kind: DatabaseInstance
metadata: { name: pg-eu2 }
spec:
  engine: postgres
  version: "17-alpine"
  resources: { memory: 2Gi, cpu: "1" }
  placement: { location: eu2 }
  adopt: true
  recoveryPoints:
    schedule: daily
    retention: { keep: 14, days: 30 }
    requireEncryption: true
---
apiVersion: miabi.io/v1
kind: Database
metadata: { name: posta-mail-db }
spec:
  engine: postgres
  instance: { type: shared, name: pg-eu2 }
  backup:
    schedule: "15 2 * * *"
    retention: { keep: 7 }
`

func TestDatabaseInstanceParses(t *testing.T) {
	set, err := d.Parse([]byte(sharedPostgres))
	if err != nil {
		t.Fatal(err)
	}
	di, ok := set.Get("DatabaseInstance/pg-eu2")
	if !ok || di.DatabaseInstance == nil || !di.DatabaseInstance.Adopt || di.DatabaseInstance.Location() != "eu2" {
		t.Fatalf("instance = %+v", di.DatabaseInstance)
	}
	if rp := di.DatabaseInstance.RecoveryPoints; !rp.On() || rp.Retention.Keep != 14 || !rp.RequireEncryption {
		t.Errorf("recoveryPoints = %+v", rp)
	}
	db, _ := set.Get("Database/posta-mail-db")
	if db.Database.Instance != (d.DatabaseInstanceRef{Type: "shared", Name: "pg-eu2"}) {
		t.Errorf("instance ref = %+v", db.Database.Instance)
	}
	if !db.Database.Backup.On() || db.Database.Backup.Schedule != "15 2 * * *" {
		t.Errorf("backup = %+v", db.Database.Backup)
	}
}

// Manifests written before instance became a block must keep round-tripping in the short form.
func TestDatabaseInstanceRefRoundTrip(t *testing.T) {
	for _, tc := range []struct{ spec, want string }{
		{"  instance: shared\n", "instance: shared"},
		{"  instance: { name: pg-eu2 }\n", "name: pg-eu2"},
	} {
		set, err := d.Parse(dbManifest(tc.spec))
		if err != nil {
			t.Fatal(err)
		}
		out, err := d.Marshal(set)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), tc.want) {
			t.Errorf("marshal of %q lacks %q:\n%s", tc.spec, tc.want, out)
		}
		if _, err := d.Parse(out); err != nil {
			t.Errorf("re-parse: %v", err)
		}
	}
}

func TestDatabaseInstanceRefusals(t *testing.T) {
	instance := func(spec string) []byte {
		return []byte("apiVersion: miabi.io/v1\nkind: DatabaseInstance\nmetadata: { name: inst }\nspec:\n" + spec)
	}
	for _, tc := range []struct {
		name, want string
		doc        []byte
	}{
		{"redis instance", "declare it as kind: Database", instance("  engine: redis\n")},
		{"libsql instance", "declare it as kind: Database", instance("  engine: libsql\n")},
		{"bad schedule", "five-field cron", instance("  engine: postgres\n  recoveryPoints: { schedule: nightly }\n")},
		{"concurrency", "concurrency", instance("  engine: postgres\n  recoveryPoints: { concurrency: 99 }\n")},
		{"redis instance.name", "instance.name is not accepted", dbManifestEngine("redis", "  instance: { name: cache }\n")},
		{"resources with name", "refused with instance.name", dbManifest("  instance: { name: pg }\n  resources: { memory: 1Gi }\n")},
		{"redis backup", "not available for redis", dbManifestEngine("redis", "  backup: { schedule: daily }\n")},
		{"bad backup schedule", "five-field cron", dbManifest("  backup: { schedule: \"* *\" }\n")},
		{"negative retention", "must not be negative", dbManifest("  backup: { retention: { keep: -1 } }\n")},
		{"bad type", "invalid instance type", dbManifest("  instance: { type: maybe }\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Parse(tc.doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDisabledBackupOnRedisIsAccepted(t *testing.T) {
	if _, err := d.Parse(dbManifestEngine("redis", "  backup: { enabled: false }\n")); err != nil {
		t.Error(err)
	}
}

// An inline database would provision an instance with the declared instance's name.
func TestInlineDatabaseNamedLikeInstanceIsRefused(t *testing.T) {
	doc := "apiVersion: miabi.io/v1\nkind: DatabaseInstance\nmetadata: { name: pg }\nspec: { engine: postgres }\n---\n" + string(dbManifest(""))
	if _, err := d.Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), "instance.name: pg") {
		t.Errorf("err = %v", err)
	}
	if _, err := d.Parse([]byte(doc + "  instance: { name: pg }\n")); err != nil {
		t.Errorf("targeting it by name: %v", err)
	}
}

func dbManifestEngine(engine, spec string) []byte {
	return []byte("apiVersion: miabi.io/v1\nkind: Database\nmetadata:\n  name: pg\nspec:\n  engine: " + engine + "\n" + spec)
}

func dbResource(spec d.DatabaseSpec) d.Resource {
	return d.Resource{APIVersion: d.APIVersion, Kind: d.KindDatabase, Metadata: d.Meta{Name: "pg"}, Database: &spec}
}

func diff(desired, actual d.Resource) []d.FieldDiff {
	ds, as := d.NewResourceSet(), d.NewResourceSet()
	ds.Add(desired)
	as.Add(actual)
	for _, ch := range d.BuildPlan(ds, as, d.PlanOptions{}).Changes {
		return ch.Fields
	}
	return nil
}

func TestBackupDiff(t *testing.T) {
	on, off := true, false
	live := d.DatabaseSpec{Engine: "postgres", Version: "17", Instance: d.DatabaseInstanceRef{Type: "auto", Name: "pg"},
		Backup: &d.BackupScheduleSpec{Enabled: &on, Schedule: "daily", Retention: &d.RetentionSpec{Keep: 7}}}

	if f := diff(dbResource(d.DatabaseSpec{Engine: "postgres", Instance: d.DatabaseInstanceRef{Type: "auto"}}), dbResource(live)); len(f) != 0 {
		t.Errorf("an omitted block, version and instance name must keep what is live, got %+v", f)
	}
	f := diff(dbResource(d.DatabaseSpec{Engine: "postgres", Backup: &d.BackupScheduleSpec{Enabled: &off}}), dbResource(live))
	if len(f) != 1 || f[0].Field != "backup.enabled" || f[0].To != "false" {
		t.Errorf("enabled: false = %+v", f)
	}
	f = diff(dbResource(d.DatabaseSpec{Engine: "postgres", Backup: &d.BackupScheduleSpec{Retention: &d.RetentionSpec{Keep: 7}}}), dbResource(live))
	if len(f) != 0 {
		t.Errorf("an omitted schedule is daily, got %+v", f)
	}
	noSchedule := live
	noSchedule.Backup = &d.BackupScheduleSpec{Enabled: &off}
	if f := diff(dbResource(d.DatabaseSpec{Engine: "postgres", Backup: &d.BackupScheduleSpec{Enabled: &off}}), dbResource(noSchedule)); len(f) != 0 {
		t.Errorf("disabled against none = %+v", f)
	}
	f = diff(dbResource(d.DatabaseSpec{Engine: "postgres", Instance: d.DatabaseInstanceRef{Name: "other"}}), dbResource(live))
	if len(f) != 1 || f[0].Field != "instance.name" {
		t.Errorf("instance.name change = %+v", f)
	}
}

func TestDatabaseInstanceApplyOrder(t *testing.T) {
	set, err := d.Parse([]byte(sharedPostgres))
	if err != nil {
		t.Fatal(err)
	}
	plan := d.BuildPlan(set, d.NewResourceSet(), d.PlanOptions{})
	if len(plan.Changes) != 2 || plan.Changes[0].Kind != d.KindDatabaseInstance {
		t.Fatalf("the instance must be created before the databases on it: %+v", plan.Changes)
	}
	removed := d.BuildPlan(d.NewResourceSet(), set, d.PlanOptions{Prune: true})
	if len(removed.Changes) != 2 || removed.Changes[0].Kind != d.KindDatabase {
		t.Fatalf("databases must be removed before their instance: %+v", removed.Changes)
	}
}
