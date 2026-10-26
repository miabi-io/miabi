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

package billing

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func planSyncService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the plan sync test")
	}
	schema := fmt.Sprintf("t_billing_plans_%d", time.Now().UnixNano())
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP SCHEMA " + schema + " CASCADE") })
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Plan{}, &models.Workspace{}); err != nil {
		t.Fatal(err)
	}
	return NewService(db, nil, nil, nil, nil, EnrolmentInfo{}), db
}

func TestPushPlanCreateUpdateAndIgnore(t *testing.T) {
	s, db := planSyncService(t)
	in := PlanPush{Name: "Starter", Limits: map[string]int64{"max_apps": 5, "max_bogus": 1}, Capabilities: map[string]bool{"allow_gpu": true},
		Settings: map[string]any{"description": "Small", "placement": map[string]any{"cluster_id": 9}}}

	res, _, err := s.PushPlan("starter", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Plan.Key != "starter" || !res.Plan.Managed || res.Plan.Limits["max_apps"] != 5 || !res.Plan.Capabilities["allow_gpu"] {
		t.Fatalf("result = %+v", res)
	}
	if !slices.Equal(res.Ignored, []string{"max_bogus", "placement"}) {
		t.Errorf("ignored = %v, want max_bogus and placement", res.Ignored)
	}

	var p models.Plan
	db.Where("external_key = ?", "starter").First(&p)
	p.StorageClasses = []string{"hdd"}
	db.Save(&p)

	in.Name, in.Limits = "Starter v2", map[string]int64{"max_apps": 10}
	res, _, err = s.PushPlan("starter", in, false)
	if err != nil {
		t.Fatal(err)
	}
	db.First(&p, p.ID)
	if res.Created || p.Name != "Starter v2" || p.MaxApps != 10 || !slices.Equal(p.StorageClasses, []string{"hdd"}) {
		t.Errorf("after update: created=%v plan=%+v (storage classes must survive)", res.Created, p)
	}
}

func TestPushPlanAdoptsAndRefuses(t *testing.T) {
	s, db := planSyncService(t)
	db.Create(&models.Plan{Name: "Pro", IsActive: true, MaxApps: 3})
	db.Create(&models.Plan{Name: "System", System: true})

	res, _, err := s.PushPlan("pro", PlanPush{Name: "Pro", Limits: map[string]int64{"max_apps": 20}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Adopted || res.Plan.Limits["max_apps"] != 20 {
		t.Fatalf("adopt: %+v", res)
	}
	if _, _, err := s.PushPlan("sys", PlanPush{Name: "System"}, false); !errors.Is(err, ErrPlanSystem) {
		t.Errorf("system plan: %v, want ErrPlanSystem", err)
	}
	if _, _, err := s.PushPlan("pro-2", PlanPush{Name: "Pro"}, false); !errors.Is(err, ErrPlanNameTaken) {
		t.Errorf("name held by another key: %v, want ErrPlanNameTaken", err)
	}
	if _, _, err := s.PushPlan("Bad Key", PlanPush{Name: "X"}, false); !errors.Is(err, ErrPlanKey) {
		t.Errorf("bad key: %v, want ErrPlanKey", err)
	}
}

func TestPushPlanDryRunWritesNothing(t *testing.T) {
	s, db := planSyncService(t)
	s.SetAccountDeps(AccountDeps{Meter: fakeMeter{"apps": 8}})
	if _, _, err := s.PushPlan("team", PlanPush{Name: "Team", Limits: map[string]int64{"max_apps": 10}}, false); err != nil {
		t.Fatal(err)
	}
	var p models.Plan
	db.Where("external_key = ?", "team").First(&p)
	db.Create(&models.Workspace{Name: "acme", DisplayName: "Acme", PlanID: &p.ID})

	_, dry, err := s.PushPlan("team", PlanPush{Name: "Team", Limits: map[string]int64{"max_apps": 5}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Workspaces) != 1 || len(dry.Workspaces[0].Violations) != 1 || dry.Workspaces[0].Violations[0].Resource != "max_apps" {
		t.Fatalf("dry run = %+v", dry)
	}
	db.First(&p, p.ID)
	if p.MaxApps != 10 {
		t.Errorf("max_apps = %d after a dry run, want 10", p.MaxApps)
	}
}

func TestRemovePlan(t *testing.T) {
	s, db := planSyncService(t)
	for _, k := range []string{"used", "unused"} {
		if _, _, err := s.PushPlan(k, PlanPush{Name: k}, false); err != nil {
			t.Fatal(err)
		}
	}
	var used models.Plan
	db.Where("external_key = ?", "used").First(&used)
	db.Create(&models.Workspace{Name: "w", DisplayName: "W", PlanID: &used.ID})

	if deleted, err := s.RemovePlan("used"); err != nil || deleted {
		t.Fatalf("used plan: deleted=%v err=%v, want deactivated", deleted, err)
	}
	db.First(&used, used.ID)
	if used.IsActive {
		t.Error("a used plan stayed active after removal")
	}
	if deleted, err := s.RemovePlan("unused"); err != nil || !deleted {
		t.Fatalf("unused plan: deleted=%v err=%v, want deleted", deleted, err)
	}
	if _, err := s.RemovePlan("nope"); !errors.Is(err, ErrPlanNotManaged) {
		t.Errorf("unknown key: %v, want ErrPlanNotManaged", err)
	}
}

func TestKeepConsoleFields(t *testing.T) {
	key := "pro"
	before := models.Plan{Name: "Pro", MaxApps: 10, AllowGPU: true, ManagedBy: models.ManagedByConsole, ExternalKey: &key}
	edited := before
	edited.Name, edited.MaxApps, edited.AllowGPU, edited.StorageClasses = "Renamed", 99, false, []string{"hdd"}

	if err := edited.KeepConsoleFields(&before); err != nil {
		t.Fatal(err)
	}
	if edited.Name != "Pro" || edited.MaxApps != 10 || !edited.AllowGPU || !slices.Equal(edited.StorageClasses, []string{"hdd"}) {
		t.Errorf("after keep: %+v", edited)
	}
}
