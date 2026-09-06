// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package migration

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// legacyBackup is the backups table as it stood before the number column, so the
// test starts from the schema a real upgrade actually finds.
type legacyBackup struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	DatabaseID  uint
	Filename    string
}

func (legacyBackup) TableName() string { return "backups" }

// TestBackfillBackupNumbers pins the upgrade path: the result must be unique per database,
// or the index AutoMigrate adds next cannot be created.
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
