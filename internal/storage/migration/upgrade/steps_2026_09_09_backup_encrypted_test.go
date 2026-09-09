// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// backupFixture mirrors the columns the backfill reads and writes.
type backupFixture struct {
	ID        uint `gorm:"primaryKey"`
	Filename  string
	Encrypted bool
}

func (backupFixture) TableName() string { return "backups" }

func TestBackupEncryptedBackfill(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&backupFixture{}); err != nil {
		t.Fatal(err)
	}

	rows := []backupFixture{
		{ID: 1, Filename: "app_20260101.sql.gz.gpg"},
		{ID: 2, Filename: "app_20260101.sql.gz"},
		{ID: 3, Filename: "app_20260101.archive.gz.gpg"},
		{ID: 4, Filename: ""},
		{ID: 5, Filename: "app_20260101.sql.gz.gpg", Encrypted: true},
		// A backup whose name merely contains ".gpg" is not an encrypted artifact:
		// only the suffix is the tools' signal.
		{ID: 6, Filename: "gpg-notes.sql.gz"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	if err := backupEncryptedStep(context.Background(), db); err != nil {
		t.Fatalf("step: %v", err)
	}

	want := map[uint]bool{1: true, 2: false, 3: true, 4: false, 5: true, 6: false}
	var got []backupFixture
	if err := db.Order("id").Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("read back %d rows, want %d", len(got), len(want))
	}
	for _, r := range got {
		if r.Encrypted != want[r.ID] {
			t.Errorf("row %d (%q): encrypted = %v, want %v", r.ID, r.Filename, r.Encrypted, want[r.ID])
		}
	}
}

// The step must be safe to run against a schema that predates the column, since
// upgrade steps run before every deployment regardless of where it is coming from.
func TestBackupEncryptedBackfillWithoutTheColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	type oldBackup struct {
		ID       uint `gorm:"primaryKey"`
		Filename string
	}
	if err := db.Table("backups").AutoMigrate(&oldBackup{}); err != nil {
		t.Fatal(err)
	}
	if err := backupEncryptedStep(context.Background(), db); err != nil {
		t.Fatalf("step on a pre-column schema: %v", err)
	}
}
