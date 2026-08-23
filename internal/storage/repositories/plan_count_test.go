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

func TestCountIncludesSystemPlans(t *testing.T) {
	repo := newPlanRepo(t)

	n, err := repo.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("Count() = %d, want 3 — the system plan counts toward the cap", n)
	}
}
