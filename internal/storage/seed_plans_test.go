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

package storage

import (
	"testing"

	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func seedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Plan{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func plans(t *testing.T, db *gorm.DB) []models.Plan {
	t.Helper()
	var out []models.Plan
	if err := db.Order("id").Find(&out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSeededCatalogFitsTheCommunityCap(t *testing.T) {
	db := seedDB(t)
	if err := SeedPlans(db); err != nil {
		t.Fatal(err)
	}

	got := len(plans(t, db))
	if got > enterprise.CommunityPlanLimit {
		t.Errorf("seed creates %d plans, above the Community cap of %d — a fresh install would boot over its own limit",
			got, enterprise.CommunityPlanLimit)
	}

	if got >= enterprise.CommunityPlanLimit {
		t.Errorf("seed fills the Community cap exactly (%d of %d); an operator could never add a plan of their own",
			got, enterprise.CommunityPlanLimit)
	}
}

func TestSeededUnlimitedPlanIsASystemPlan(t *testing.T) {
	db := seedDB(t)
	if err := SeedPlans(db); err != nil {
		t.Fatal(err)
	}
	var unlimited *models.Plan
	for i, p := range plans(t, db) {
		if p.Name == models.UnlimitedPlanName {
			unlimited = &plans(t, db)[i]
		}
	}
	if unlimited == nil {
		t.Fatalf("the %s plan is not seeded — the system workspace would fall back to the default plan's limits",
			models.UnlimitedPlanName)
	}
	if !unlimited.System {
		t.Errorf("the %s plan is not marked System, so nothing stops it being renamed, deleted or made the default",
			models.UnlimitedPlanName)
	}
}

// Workspaces without a plan land on the default, so exactly one non-system plan must carry the flag.
func TestSeededCatalogHasExactlyOneDefault(t *testing.T) {
	db := seedDB(t)
	if err := SeedPlans(db); err != nil {
		t.Fatal(err)
	}
	var defaults []string
	for _, p := range plans(t, db) {
		if p.IsDefault {
			defaults = append(defaults, p.Name)
		}
		if p.IsDefault && p.System {
			t.Errorf("%q is both the default and a system plan — tenants would land on the platform's own plan", p.Name)
		}
	}
	if len(defaults) != 1 {
		t.Errorf("seed has %d default plans (%v), want exactly 1", len(defaults), defaults)
	}
}

func TestSeedPlansIsIdempotent(t *testing.T) {
	db := seedDB(t)
	if err := SeedPlans(db); err != nil {
		t.Fatal(err)
	}
	before := len(plans(t, db))

	if err := db.Where("name = ?", "Pro").Delete(&models.Plan{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Plan{Name: "Starter", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	if err := SeedPlans(db); err != nil {
		t.Fatal(err)
	}
	after := plans(t, db)
	if len(after) != before {
		t.Errorf("re-seeding changed the catalog size from %d to %d", before, len(after))
	}
	for _, p := range after {
		if p.Name == "Pro" {
			t.Error("re-seeding restored a plan the operator had deleted")
		}
	}
}
