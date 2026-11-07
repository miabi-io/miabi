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

package migration

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type legacyBackup struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	DatabaseID  uint
	Filename    string
}

func (legacyBackup) TableName() string { return "backups" }

// The result must be unique per database or the index AutoMigrate adds next can't be created.
func TestBackfillBackupNumbers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&legacyBackup{}); err != nil {
		t.Fatalf("migrate legacy: %v", err)
	}
	// Interleave two databases so ids and per-database numbers diverge.
	for _, dbID := range []uint{1, 1, 2, 1, 2} {
		if err := db.Create(&legacyBackup{WorkspaceID: 7, DatabaseID: dbID}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := backfillBackupNumbers(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var got []models.Backup
	if err := db.Order("id").Find(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	want := []int{1, 2, 1, 3, 2}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Number != w {
			t.Errorf("row %d (database %d): Number = %d, want %d", got[i].ID, got[i].DatabaseID, got[i].Number, w)
		}
	}

	// The numbering must be unique per database, or AutoMigrate can't add the index.
	if err := db.Exec(`CREATE UNIQUE INDEX idx_backup_db_number ON backups (database_id, number)`).Error; err != nil {
		t.Fatalf("unique index over backfilled rows: %v", err)
	}

	// Rerunning must not renumber anything.
	if err := backfillBackupNumbers(db); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	var again []models.Backup
	if err := db.Order("id").Find(&again).Error; err != nil {
		t.Fatalf("read back again: %v", err)
	}
	for i, w := range want {
		if again[i].Number != w {
			t.Errorf("rerun changed row %d: Number = %d, want %d", again[i].ID, again[i].Number, w)
		}
	}
}

func TestBackfillBackupNumbersNoTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := backfillBackupNumbers(db); err != nil {
		t.Fatalf("fresh install: %v", err)
	}
}

type legacyCronJob struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	Name        string
}

func (legacyCronJob) TableName() string { return "cron_jobs" }

func TestSlugifyCronJobNames(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&legacyCronJob{}); err != nil {
		t.Fatalf("migrate legacy: %v", err)
	}
	seed := []legacyCronJob{
		{WorkspaceID: 1, Name: "Nightly Report"},
		{WorkspaceID: 1, Name: "nightly-report"},
		{WorkspaceID: 1, Name: ""},
		{WorkspaceID: 2, Name: "Nightly Report"},
		{WorkspaceID: 1, Name: "cleanup"},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := slugifyCronJobNames(db); err != nil {
		t.Fatal(err)
	}
	var got []struct {
		ID          uint
		Name        string
		DisplayName string
	}
	db.Table("cron_jobs").Select("id, name, display_name").Order("id").Scan(&got)
	want := []struct{ name, display string }{
		{"nightly-report", "Nightly Report"},
		{"nightly-report-2", "nightly-report"},
		{"cronjob", ""},
		{"nightly-report", "Nightly Report"},
		{"cleanup", ""},
	}
	for i, w := range want {
		if got[i].Name != w.name || got[i].DisplayName != w.display {
			t.Errorf("row %d = %q/%q, want %q/%q", got[i].ID, got[i].Name, got[i].DisplayName, w.name, w.display)
		}
	}
	if err := slugifyCronJobNames(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
