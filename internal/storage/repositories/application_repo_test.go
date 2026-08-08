// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// appLabelRow is a sqlite-friendly stand-in for models.Application: the full model carries
// Postgres-specific column defaults sqlite can't migrate, and ExternalLabelTaken only reads
// id and external_label.
type appLabelRow struct {
	ID            uint `gorm:"primaryKey"`
	WorkspaceID   uint
	ExternalLabel string
}

func (appLabelRow) TableName() string { return "applications" }

func newAppLabelDB(t *testing.T) *ApplicationRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&appLabelRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApplicationRepository(db)
}

func TestExternalLabelTaken(t *testing.T) {
	repo := newAppLabelDB(t)
	// Two apps in different workspaces; the label space is platform-wide.
	rows := []appLabelRow{
		{ID: 1, WorkspaceID: 10, ExternalLabel: "okapi"},
		{ID: 2, WorkspaceID: 20, ExternalLabel: "blog-eqi3tlf2"},
		{ID: 3, WorkspaceID: 30, ExternalLabel: ""}, // unlabeled app
	}
	if err := repo.db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name   string
		label  string
		except uint
		want   bool
	}{
		{"taken by another app", "okapi", 2, true},
		{"taken across workspaces", "okapi", 3, true},
		{"excludes the app itself", "okapi", 1, false},
		{"free label", "frontend", 1, false},
		{"empty label never matches the unlabeled app", "", 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.ExternalLabelTaken(tc.label, tc.except)
			if err != nil {
				t.Fatalf("ExternalLabelTaken: %v", err)
			}
			if got != tc.want {
				t.Errorf("ExternalLabelTaken(%q, except=%d) = %v, want %v", tc.label, tc.except, got, tc.want)
			}
		})
	}
}
