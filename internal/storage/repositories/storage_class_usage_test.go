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
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Usage lists both volumes and database instances on a class, largest first, and nothing from other classes;
// a database never sized (NULL) reads as 0.
func TestStorageClassUsage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "usage.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Raw tables: the models' uid column defaults to Postgres-only gen_random_uuid(), which SQLite rejects.
	for _, q := range []string{
		`CREATE TABLE workspaces (id integer PRIMARY KEY, name text, display_name text)`,
		`CREATE TABLE volumes (id integer PRIMARY KEY, uid text, name text, display_name text, workspace_id integer,
			server_id integer, used_bytes integer, used_measured_at datetime, created_at datetime, storage_class_name text)`,
		`CREATE TABLE database_instances (id integer PRIMARY KEY, uid text, name text, display_name text, workspace_id integer,
			server_id integer, size_bytes integer, size_synced_at datetime, created_at datetime, storage_class_name text)`,
		`INSERT INTO workspaces VALUES (1, 'acme', 'Acme')`,
		`INSERT INTO volumes VALUES (1, 'u-small', 'small', NULL, 1, 0, 10, NULL, '2026-10-29', 'ssd')`,
		`INSERT INTO volumes VALUES (2, 'u-else', 'elsewhere', NULL, 1, 0, 999, NULL, '2026-10-29', 'default')`,
		`INSERT INTO database_instances VALUES (1, 'u-pg', 'pg', 'Postgres', 1, 2, NULL, NULL, '2026-10-29', 'ssd')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}

	repo := NewStorageClassRepository(db)
	for class, want := range map[string]int64{"ssd": 1, "default": 0} {
		if n, err := repo.CountDatabases(class); err != nil || n != want {
			t.Errorf("CountDatabases(%q) = %d, %v; want %d", class, n, err, want)
		}
	}

	got, err := repo.Usage("ssd")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want the volume and the database on ssd: %+v", len(got), got)
	}
	if got[0].Kind != "volume" || got[0].Name != "small" || got[0].UsedBytes != 10 || got[0].WorkspaceName != "Acme" {
		t.Errorf("largest first: %+v", got[0])
	}
	if got[1].Kind != "database" || got[1].Name != "pg" || got[1].UsedBytes != 0 || got[1].ServerID != 2 || got[1].UID != "u-pg" {
		t.Errorf("volume row: %+v", got[1])
	}
}
