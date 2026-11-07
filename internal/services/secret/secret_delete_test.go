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

package secret

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDeleteRefusesManagedSecret(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:secretdel?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The UIDModel's Postgres gen_random_uuid() default breaks sqlite AutoMigrate, so the table is created by hand.
	if err := db.Exec(`CREATE TABLE secrets (
		uid text, id integer PRIMARY KEY AUTOINCREMENT, workspace_id integer,
		name text, display_name text, value_enc text, description text,
		version integer DEFAULT 1, updated_by_id integer, managed integer DEFAULT 0,
		owner_kind text, owner_id integer, sealed_fp text, sealed_key_version integer DEFAULT 0, metadata text, created_at datetime, updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	managed := &models.Secret{WorkspaceID: 1, Name: "db_managed", ValueEnc: "x", Managed: true}
	managed.UID = "uid-managed"
	if err := db.Create(managed).Error; err != nil {
		t.Fatalf("create managed: %v", err)
	}
	plain := &models.Secret{WorkspaceID: 1, Name: "api_key", ValueEnc: "x"}
	plain.UID = "uid-plain"
	if err := db.Create(plain).Error; err != nil {
		t.Fatalf("create plain: %v", err)
	}

	svc := NewService(repositories.NewSecretRepository(db))
	if err := svc.Delete(1, managed.ID); err != ErrManaged {
		t.Errorf("Delete(managed) = %v, want ErrManaged", err)
	}
	if err := svc.Delete(1, plain.ID); err != nil {
		t.Errorf("Delete(plain) = %v, want nil", err)
	}
}
