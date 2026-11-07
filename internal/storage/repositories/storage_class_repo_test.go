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

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Mirrors models.StorageClass without the Postgres-only uid default sqlite cannot parse.
type storageClassTable struct {
	ID             uint `gorm:"primaryKey"`
	UID            string
	Name           string `gorm:"uniqueIndex"`
	DisplayName    string
	Description    string
	ServerID       uint
	ClusterID      uint
	Path           string
	Shared         bool
	IsDefault      bool
	Enabled        bool
	ReclaimPolicy  string
	Builtin        bool
	CapacityBytes  int64
	AvailableBytes int64
	MeasuredAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (storageClassTable) TableName() string { return "storage_classes" }

func newStorageClassDB(t *testing.T) *StorageClassRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&storageClassTable{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewStorageClassRepository(db)
}

// The edition cap bounds the whole catalog, so the seeded built-in class counts.
func TestStorageClassCountIncludesTheBuiltin(t *testing.T) {
	repo := newStorageClassDB(t)

	n, err := repo.Count()
	if err != nil {
		t.Fatalf("count on an empty catalog: %v", err)
	}
	if n != 0 {
		t.Fatalf("Count() = %d on an empty catalog, want 0", n)
	}

	if err := repo.Create(&models.StorageClass{Name: models.DefaultStorageClassName, Builtin: true, Enabled: true, IsDefault: true}); err != nil {
		t.Fatalf("seed builtin: %v", err)
	}
	if err := repo.Create(&models.StorageClass{Name: "ssd-fast", Path: "/mnt/ssd1/miabi", Enabled: true}); err != nil {
		t.Fatalf("create class: %v", err)
	}

	n, err = repo.Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("Count() = %d, want 2 (built-in included)", n)
	}
}
