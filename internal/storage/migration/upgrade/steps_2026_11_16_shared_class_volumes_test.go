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

type sharedVolumeFixture struct {
	ID               uint `gorm:"primaryKey"`
	Driver           string
	AccessMode       string
	StorageClassName string
}

func (sharedVolumeFixture) TableName() string { return "volumes" }

type sharedClassFixture struct {
	ID     uint `gorm:"primaryKey"`
	Name   string
	Path   string
	Shared bool
}

func (sharedClassFixture) TableName() string { return "storage_classes" }

func TestSharedClassVolumesStep(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&sharedVolumeFixture{}, &sharedClassFixture{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&[]sharedClassFixture{
		{ID: 1, Name: "replicated", Path: "/mnt/shared/miabi", Shared: true},
		{ID: 2, Name: "ssd", Path: "/mnt/ssd1", Shared: false},
		{ID: 3, Name: "default", Path: "", Shared: true},
	})
	db.Create(&[]sharedVolumeFixture{
		{ID: 1, Driver: "local", AccessMode: "rwo", StorageClassName: "replicated"},
		{ID: 2, Driver: "local", AccessMode: "rwo", StorageClassName: "ssd"},
		{ID: 3, Driver: "local", AccessMode: "rwo", StorageClassName: "default"},
		{ID: 4, Driver: "nfs", AccessMode: "rwx", StorageClassName: "default"},
	})
	for range 2 {
		if err := sharedClassVolumesStep(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	want := map[uint]string{1: "rwx", 2: "rwo", 3: "rwo", 4: "rwx"}
	var rows []sharedVolumeFixture
	db.Order("id").Find(&rows)
	for _, r := range rows {
		if r.AccessMode != want[r.ID] {
			t.Errorf("volume %d access mode = %s, want %s", r.ID, r.AccessMode, want[r.ID])
		}
	}
}
