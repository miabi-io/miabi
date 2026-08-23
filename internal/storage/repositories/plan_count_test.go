// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newPlanRepo(t *testing.T) *PlanRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Plan{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rows := []models.Plan{
		{Name: "Pro", IsActive: true, IsDefault: true},
		{Name: "Starter", IsActive: true},
		{Name: models.UnlimitedPlanName, IsActive: true, System: true},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	return NewPlanRepository(db)
}

// Count backs the edition's plan cap, so it counts the catalog an operator
// publishes — not the platform's own plan, which nobody created and which would
// otherwise cost them a slot.
func TestCountExcludesSystemPlans(t *testing.T) {
	repo := newPlanRepo(t)

	n, err := repo.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("Count() = %d, want 2 (the system plan must not count)", n)
	}

	all, err := repo.CountAll()
	if err != nil {
		t.Fatal(err)
	}
	if all != 3 {
		t.Errorf("CountAll() = %d, want 3", all)
	}
}
