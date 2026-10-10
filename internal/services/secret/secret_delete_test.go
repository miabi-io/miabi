// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package secret

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestDeleteRefusesManagedSecret verifies a managed secret cannot be hand-deleted
// (its lifecycle follows its owning resource), while a plain secret with no
// referencing apps deletes normally.
func TestDeleteRefusesManagedSecret(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:secretdel?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The Secret model's UIDModel carries a Postgres `gen_random_uuid()` default
	// that sqlite's AutoMigrate can't parse, so create a compatible table by hand.
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
