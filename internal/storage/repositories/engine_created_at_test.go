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

package repositories

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The full models carry Postgres-specific column defaults sqlite can't migrate.
type volumeStampRow struct {
	ID              uint `gorm:"primaryKey"`
	DockerName      string
	EngineCreatedAt string
	UpdatedAt       time.Time
}

func (volumeStampRow) TableName() string { return "volumes" }

type instanceStampRow struct {
	ID                    uint `gorm:"primaryKey"`
	Name                  string
	VolumeEngineCreatedAt string
	UpdatedAt             time.Time
}

func (instanceStampRow) TableName() string { return "database_instances" }

func newStampDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&volumeStampRow{}, &instanceStampRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// Keeping the first timestamp makes a volume recreated by hand read as replaced instead of intact.
func TestSetEngineCreatedAtWritesOnlyOnce(t *testing.T) {
	db := newStampDB(t)
	repo := NewVolumeRepository(db)
	if err := db.Create(&volumeStampRow{ID: 3, DockerName: "mb-vol-1-data"}).Error; err != nil {
		t.Fatal(err)
	}

	if err := repo.SetEngineCreatedAt(3, "2026-09-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEngineCreatedAt(3, "2026-09-16T02:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// An empty timestamp is nothing to record, not a reason to clear what is there.
	if err := repo.SetEngineCreatedAt(3, ""); err != nil {
		t.Fatal(err)
	}

	var got volumeStampRow
	if err := db.First(&got, 3).Error; err != nil {
		t.Fatal(err)
	}
	if got.EngineCreatedAt != "2026-09-01T10:00:00Z" {
		t.Fatalf("engine_created_at = %q; want the first one recorded", got.EngineCreatedAt)
	}
}

func TestSetVolumeEngineCreatedAtWritesOnlyOnce(t *testing.T) {
	db := newStampDB(t)
	repo := NewDatabaseRepository(db)
	if err := db.Create(&instanceStampRow{ID: 5, Name: "main"}).Error; err != nil {
		t.Fatal(err)
	}

	if err := repo.SetVolumeEngineCreatedAt(5, "2026-09-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetVolumeEngineCreatedAt(5, "2026-09-16T02:00:00Z"); err != nil {
		t.Fatal(err)
	}

	var got instanceStampRow
	if err := db.First(&got, 5).Error; err != nil {
		t.Fatal(err)
	}
	if got.VolumeEngineCreatedAt != "2026-09-01T10:00:00Z" {
		t.Fatalf("volume_engine_created_at = %q; want the first one recorded", got.VolumeEngineCreatedAt)
	}
}
