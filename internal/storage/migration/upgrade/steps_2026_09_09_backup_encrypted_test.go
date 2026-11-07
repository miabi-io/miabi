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
		// Only the suffix is the tools' signal.
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

// Upgrade steps run before every deployment, whatever schema it is coming from.
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
