// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Sqlite-friendly stand-ins: the full models carry Postgres-specific column defaults sqlite can't
// migrate, and these writes touch one column each.
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

// The first timestamp is the one that matters: keeping it is what makes a volume that was deleted and
// recreated by hand read as replaced instead of intact.
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
