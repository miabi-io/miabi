// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// canaryRow is a sqlite-friendly stand-in for the applications table: the full
// model carries Postgres-specific column defaults sqlite can't migrate, and the
// canary calls only touch these columns. Reads and writes still go through the
// real models.Application schema, so the JSON serializer is exercised.
type canaryRow struct {
	ID              uint `gorm:"primaryKey"`
	WorkspaceID     uint
	CanaryReleaseID *uint
	CanaryWeight    int
	CanaryMode      string
	CanaryExclusive bool
	CanaryPriority  int
	CanaryMatch     string
	CanaryPausedAt  *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (canaryRow) TableName() string { return "applications" }

func newCanaryDB(t *testing.T) (*ApplicationRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&canaryRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&canaryRow{ID: 1, WorkspaceID: 1, CanaryMode: string(models.CanaryModeAuto)}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	return NewApplicationRepository(db), db
}

func loadCanary(t *testing.T, db *gorm.DB) models.Application {
	t.Helper()
	var app models.Application
	if err := db.Session(&gorm.Session{}).Model(&models.Application{}).
		Select("id", "canary_release_id", "canary_weight", "canary_mode", "canary_exclusive", "canary_priority", "canary_match", "canary_paused_at").
		Where("id = ?", 1).Take(&app).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	return app
}

func TestSetCanaryRoutingRoundTrips(t *testing.T) {
	repo, db := newCanaryDB(t)
	rules := []models.CanaryMatchRule{
		{Source: "header", Name: "X-Canary", Operator: "equals", Value: "true"},
		{Source: "cookie", Name: "beta_user", Operator: "in", Value: "admin,tester"},
	}
	if err := repo.SetCanaryRouting(1, models.CanaryModeManual, true, 7, rules); err != nil {
		t.Fatalf("SetCanaryRouting: %v", err)
	}
	got := loadCanary(t, db)
	if got.CanaryMode != models.CanaryModeManual || !got.CanaryExclusive || got.CanaryPriority != 7 {
		t.Errorf("routing did not round-trip: %+v", got)
	}
	if len(got.CanaryMatch) != 2 || got.CanaryMatch[1].Value != "admin,tester" {
		t.Errorf("match rules did not round-trip: %+v", got.CanaryMatch)
	}
}

// Returning to the automatic ramp must actually clear the flags, not skip them
// as struct-update zero values.
func TestSetCanaryRoutingClearsBackToAuto(t *testing.T) {
	repo, db := newCanaryDB(t)
	rules := []models.CanaryMatchRule{{Source: "header", Name: "X-Canary", Operator: "equals", Value: "true"}}
	if err := repo.SetCanaryRouting(1, models.CanaryModeManual, true, 7, rules); err != nil {
		t.Fatalf("SetCanaryRouting: %v", err)
	}
	if err := repo.SetCanaryRouting(1, models.CanaryModeAuto, false, 0, nil); err != nil {
		t.Fatalf("SetCanaryRouting (auto): %v", err)
	}
	got := loadCanary(t, db)
	if got.CanaryMode != models.CanaryModeAuto || got.CanaryExclusive || got.CanaryPriority != 0 || len(got.CanaryMatch) != 0 {
		t.Errorf("switching back to auto left advanced routing behind: %+v", got)
	}
}

// Promote, abort and supersede all clear the canary through SetCanary. A `match`
// surviving that would silently target the next rollout, so clearing the release
// must clear the rules with it.
func TestSetCanaryClearClearsRules(t *testing.T) {
	repo, db := newCanaryDB(t)
	relID := uint(9)
	if err := repo.SetCanary(1, &relID, 20); err != nil {
		t.Fatalf("SetCanary: %v", err)
	}
	rules := []models.CanaryMatchRule{{Source: "header", Name: "X-Canary", Operator: "equals", Value: "true"}}
	if err := repo.SetCanaryRouting(1, models.CanaryModeManual, true, 7, rules); err != nil {
		t.Fatalf("SetCanaryRouting: %v", err)
	}

	now := time.Now()
	if err := repo.SetCanaryPaused(1, &now); err != nil {
		t.Fatalf("SetCanaryPaused: %v", err)
	}

	if err := repo.SetCanary(1, nil, 0); err != nil {
		t.Fatalf("SetCanary (clear): %v", err)
	}
	got := loadCanary(t, db)
	if got.CanaryReleaseID != nil || got.CanaryWeight != 0 {
		t.Errorf("canary not cleared: %+v", got)
	}
	if len(got.CanaryMatch) != 0 || got.CanaryExclusive || got.CanaryPriority != 0 {
		t.Errorf("routing rules survived the clear: %+v", got)
	}
	if got.CanaryPausedAt != nil {
		t.Error("a paused flag survived the clear; the next rollout would start paused")
	}
	// The mode is the user's standing preference and must survive.
	if got.CanaryMode != models.CanaryModeManual {
		t.Errorf("mode = %q, want it preserved as manual", got.CanaryMode)
	}
}

// A weight change mid-rollout must leave the rules exactly where they are.
func TestSetCanaryWeightChangeKeepsRules(t *testing.T) {
	repo, db := newCanaryDB(t)
	relID := uint(9)
	rules := []models.CanaryMatchRule{{Source: "header", Name: "X-Canary", Operator: "equals", Value: "true"}}
	if err := repo.SetCanary(1, &relID, 20); err != nil {
		t.Fatalf("SetCanary: %v", err)
	}
	if err := repo.SetCanaryRouting(1, models.CanaryModeManual, true, 7, rules); err != nil {
		t.Fatalf("SetCanaryRouting: %v", err)
	}
	if err := repo.SetCanary(1, &relID, 50); err != nil {
		t.Fatalf("SetCanary (weight): %v", err)
	}
	got := loadCanary(t, db)
	if got.CanaryWeight != 50 {
		t.Errorf("weight = %d, want 50", got.CanaryWeight)
	}
	if len(got.CanaryMatch) != 1 || !got.CanaryExclusive || got.CanaryPriority != 7 {
		t.Errorf("a weight change dropped the rules: %+v", got)
	}
}
