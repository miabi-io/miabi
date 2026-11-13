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

package apply

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/robfig/cron/v3"
)

func TestResolveScheduleSpreadsPresets(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		c := resolveSchedule(1, declarative.KindDatabase, name, declarative.SchedulePresetDaily)
		if c != resolveSchedule(1, declarative.KindDatabase, name, declarative.SchedulePresetDaily) {
			t.Fatalf("%s: preset is not stable", name)
		}
		if _, err := cron.ParseStandard(c); err != nil {
			t.Fatalf("%s: %q: %v", name, c, err)
		}
		hour := strings.Fields(c)[1]
		if hour < "1" || hour > "4" {
			t.Errorf("%s: daily hour %s is outside the night window", name, hour)
		}
		seen[c] = true
	}
	if len(seen) < 4 {
		t.Errorf("eight databases share %d schedules", len(seen))
	}
	for _, p := range []string{declarative.SchedulePresetHourly, declarative.SchedulePresetWeekly} {
		if _, err := cron.ParseStandard(resolveSchedule(3, declarative.KindDatabaseInstance, "pg", p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if got := resolveSchedule(1, declarative.KindDatabase, "x", "30 3 * * 1"); got != "30 3 * * 1" {
		t.Errorf("a literal cron must be kept, got %q", got)
	}
}

func instanceSpec(adopt bool, version string) *declarative.DatabaseInstanceSpec {
	return &declarative.DatabaseInstanceSpec{Engine: "postgres", Version: version, Adopt: adopt}
}

func TestAdoptRefusal(t *testing.T) {
	s := &Service{}
	unowned := &models.DatabaseInstance{Name: "pg", Engine: models.DBEnginePostgres, Version: "17", Metadata: models.Metadata{models.MetaManagedBy: models.ManagedByUser}}
	for _, tc := range []struct {
		name string
		inst *models.DatabaseInstance
		spec *declarative.DatabaseInstanceSpec
		want string
	}{
		{"needs adopt", unowned, instanceSpec(false, ""), "set adopt: true"},
		{"adopts", unowned, instanceSpec(true, ""), ""},
		{"same version", unowned, instanceSpec(true, "17"), ""},
		{"other version", unowned, instanceSpec(true, "16"), "then upgrade"},
		{"other engine", unowned, &declarative.DatabaseInstanceSpec{Engine: "mysql", Adopt: true}, "runs postgres"},
		{"gitops", &models.DatabaseInstance{Engine: models.DBEnginePostgres, Metadata: models.Metadata{models.MetaManagedBy: models.ManagedByGitOps}}, instanceSpec(true, ""), "managed by gitops"},
		{"marketplace", &models.DatabaseInstance{Engine: models.DBEnginePostgres, Metadata: models.Metadata{models.MetaManagedBy: models.ManagedByMarketplace}}, instanceSpec(true, ""), "managed by marketplace"},
		{"inline database", &models.DatabaseInstance{Engine: models.DBEnginePostgres, Metadata: models.Metadata{
			models.MetaDeclarativeKind: "Database", models.MetaDeclarativeName: "app-db"}}, instanceSpec(true, ""), `owned by Database "app-db"`},
		{"storage class", unowned, &declarative.DatabaseInstanceSpec{Engine: "postgres", Adopt: true, StorageClass: "ssd"}, "storage class"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.adoptRefusal(tc.inst, tc.spec, "pg")
			if tc.want == "" {
				if err != nil {
					t.Errorf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if err := s.adoptRefusal(unowned, instanceSpec(false, ""), "pg"); !errors.Is(err, ErrInstanceExists) {
		t.Errorf("a missing adopt must carry INSTANCE_EXISTS, got %v", err)
	}
}

func TestCheckNamedTarget(t *testing.T) {
	s := &Service{}
	desired := declarative.NewResourceSet()
	desired.Add(declarative.Resource{APIVersion: declarative.APIVersion, Kind: declarative.KindDatabaseInstance,
		Metadata: declarative.Meta{Name: "declared"}, DatabaseInstance: &declarative.DatabaseInstanceSpec{Engine: "postgres", Version: "17"}})
	live := &models.DatabaseInstance{Name: "live", Engine: models.DBEnginePostgres, Version: "16"}
	dedicated := &models.DatabaseInstance{Name: "solo", Engine: models.DBEnginePostgres,
		Metadata: models.Metadata{models.MetaPlacement: models.PlacementValueDedicated}}
	spec := func(typ, target, version string) *declarative.DatabaseSpec {
		return &declarative.DatabaseSpec{Engine: "postgres", Version: version, Instance: declarative.DatabaseInstanceRef{Type: typ, Name: target}}
	}
	for _, tc := range []struct {
		name string
		spec *declarative.DatabaseSpec
		inst *models.DatabaseInstance
		want string
	}{
		{"live match", spec("shared", "live", ""), live, ""},
		{"live version", spec("shared", "live", "17"), live, "runs version 16"},
		{"declared match", spec("shared", "declared", "17"), nil, ""},
		{"declared engine", &declarative.DatabaseSpec{Engine: "mysql", Instance: declarative.DatabaseInstanceRef{Type: "auto", Name: "declared"}}, nil, "runs postgres"},
		{"shared missing", spec("shared", "nowhere", ""), nil, "does not exist"},
		{"auto missing provisions", spec("auto", "nowhere", ""), nil, ""},
		{"dedicated taken", spec("dedicated", "live", ""), live, "provisions its own"},
		{"dedicated host", spec("auto", "solo", ""), dedicated, "dedicated to a single dependency"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.checkNamedTarget("app-db", tc.spec, tc.inst, desired)
			if tc.want == "" {
				if err != nil {
					t.Errorf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

type fakeEE struct{ err error }

func (f fakeEE) RequireMutable(string) error { return f.err }

type fakeTarget struct{ dest backup.Destination }

func (f fakeTarget) DatabaseDestination(uint) (backup.Destination, error) { return f.dest, nil }

func TestCheckRecoveryPoints(t *testing.T) {
	on := &declarative.RecoveryPointsSpec{Schedule: "daily"}
	strict := &declarative.RecoveryPointsSpec{RequireEncryption: true}
	s3 := backup.Destination{Type: "s3"}
	sealed := backup.Destination{Type: "s3", AgeRecipients: "age1x"}
	for _, tc := range []struct {
		name string
		svc  *Service
		spec *declarative.RecoveryPointsSpec
		want string
	}{
		{"absent block", &Service{}, nil, ""},
		{"no policies", &Service{}, on, "not available"},
		{"community", &Service{backups: &BackupPolicies{Entitlements: fakeEE{errors.New("not licensed")}, Target: fakeTarget{s3}}}, on, "Enterprise"},
		{"local target", &Service{backups: &BackupPolicies{Entitlements: fakeEE{}, Target: fakeTarget{backup.Destination{Type: "local"}}}}, on, "S3"},
		{"s3", &Service{backups: &BackupPolicies{Entitlements: fakeEE{}, Target: fakeTarget{s3}}}, on, ""},
		{"cleartext", &Service{backups: &BackupPolicies{Entitlements: fakeEE{}, Target: fakeTarget{s3}}}, strict, "requireEncryption"},
		{"encrypted", &Service{backups: &BackupPolicies{Entitlements: fakeEE{}, Target: fakeTarget{sealed}}}, strict, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.svc.checkRecoveryPoints(1, "pg", tc.spec)
			if tc.want == "" {
				if err != nil {
					t.Errorf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDoubleDumpWarning(t *testing.T) {
	set, err := declarative.Parse([]byte(`apiVersion: miabi.io/v1
kind: DatabaseInstance
metadata: { name: pg }
spec: { engine: postgres, recoveryPoints: { schedule: daily } }
---
apiVersion: miabi.io/v1
kind: Database
metadata: { name: app-db }
spec: { engine: postgres, instance: { name: pg }, backup: {} }
---
apiVersion: miabi.io/v1
kind: Database
metadata: { name: quiet-db }
spec: { engine: postgres, instance: { name: pg } }
`))
	if err != nil {
		t.Fatal(err)
	}
	w := doubleDumps(set)
	if len(w) != 1 || !strings.Contains(w[0], `"app-db"`) {
		t.Errorf("warnings = %q", w)
	}
}
