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

package storageclass

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSharedFlagLocksOnceVolumesExist(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// The uid columns default to the Postgres-only gen_random_uuid(), so the tables are created by hand.
	for _, stmt := range []string{
		`CREATE TABLE storage_classes (id INTEGER PRIMARY KEY, uid TEXT, name TEXT, display_name TEXT, description TEXT,
			server_id INTEGER, cluster_id INTEGER, path TEXT, shared BOOLEAN, is_default BOOLEAN, enabled BOOLEAN,
			reclaim_policy TEXT, builtin BOOLEAN, capacity_bytes INTEGER, available_bytes INTEGER, measured_at DATETIME,
			created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE volumes (id INTEGER PRIMARY KEY, storage_class_name TEXT)`,
		`INSERT INTO storage_classes (id, name, path, shared, enabled, reclaim_policy, builtin) VALUES
			(1, 'used', '/mnt/a/miabi', false, true, 'delete', false), (2, 'empty', '/mnt/b/miabi', false, true, 'delete', false)`,
		`INSERT INTO volumes (id, storage_class_name) VALUES (1, 'used')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(repositories.NewStorageClassRepository(db), nil)
	in := Input{Shared: true, Enabled: true, ReclaimPolicy: models.ReclaimDelete}

	if _, err := s.Update(1, in); !errors.Is(err, ErrInUse) {
		t.Fatalf("sharing a class with volumes: err = %v, want ErrInUse", err)
	}
	c, err := s.Update(2, in)
	if err != nil || !c.Shared {
		t.Fatalf("sharing an empty class: %+v, %v", c, err)
	}
	in.Shared = false
	in.DisplayName = "renamed"
	if c, err := s.Update(1, in); err != nil || c.DisplayName != "renamed" {
		t.Fatalf("editing a used class without touching sharing: %+v, %v", c, err)
	}
}
