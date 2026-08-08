// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"reflect"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func newBackupSettingsDB(t *testing.T) *WorkspaceBackupSettingsRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.WorkspaceBackupSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewWorkspaceBackupSettingsRepository(db)
}

// The second save is the one that matters. A column missing from the upsert's assignment list is
// written by the insert and dropped by every update after it, so the settings look saved until
// they are read back — which is how a bundle passphrase on a configured workspace vanished.
func TestUpsertPersistsEveryFieldOnUpdate(t *testing.T) {
	repo := newBackupSettingsDB(t)

	first := &models.WorkspaceBackupSettings{WorkspaceID: 1, S3Enabled: true, S3Bucket: "acme"}
	if err := repo.Upsert(first); err != nil {
		t.Fatalf("insert: %v", err)
	}

	stored, err := repo.FindByWorkspace(1)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	stored.S3Bucket = "acme-2"
	stored.S3Region = "eu-central-1"
	stored.S3AccessKey = "AKIA"
	stored.S3SecretKeyEnc = "enc:secret"
	stored.S3Endpoint = "https://s3.example.com"
	stored.S3UseSSL = true
	stored.S3ForcePathStyle = true
	stored.DatabaseBackupPath = "backups/databases"
	stored.VolumeBackupPath = "backups/volumes"
	stored.BundlePath = "bundles"
	stored.BundlePassphraseEnc = "enc:passphrase"
	if err := repo.Upsert(stored); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.FindByWorkspace(1)
	if err != nil {
		t.Fatalf("find after update: %v", err)
	}
	if got.BundlePassphraseEnc != "enc:passphrase" {
		t.Fatalf("the bundle passphrase was dropped on update: %q", got.BundlePassphraseEnc)
	}
	if got.BundlePath != "bundles" {
		t.Fatalf("the bundle path was dropped on update: %q", got.BundlePath)
	}
	if got.S3Bucket != "acme-2" || got.S3SecretKeyEnc != "enc:secret" ||
		got.DatabaseBackupPath != "backups/databases" || got.VolumeBackupPath != "backups/volumes" ||
		got.S3Region != "eu-central-1" || got.S3AccessKey != "AKIA" ||
		got.S3Endpoint != "https://s3.example.com" || !got.S3ForcePathStyle {
		t.Fatalf("an S3 field was dropped on update: %+v", got)
	}
}

// The guard against the next field added to the model and forgotten in the
// upsert: every writable column the model declares must be assigned on conflict.
func TestUpsertAssignsEveryWritableColumn(t *testing.T) {
	assigned := map[string]bool{}
	for _, c := range backupSettingsColumns {
		assigned[c] = true
	}
	// Identity and the creation stamp are deliberately not reassigned.
	skip := map[string]bool{"id": true, "workspace_id": true, "created_at": true}

	naming := schema.NamingStrategy{}
	typ := reflect.TypeOf(models.WorkspaceBackupSettings{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("gorm")
		if tag == "-" || strings.Contains(tag, "-") && strings.TrimSpace(tag) == "-" {
			continue
		}
		col := naming.ColumnName("", f.Name)
		for _, part := range strings.Split(tag, ";") {
			if after, ok := strings.CutPrefix(strings.TrimSpace(part), "column:"); ok {
				col = after
			}
			if strings.TrimSpace(part) == "-" {
				col = "" // transient (the *_set display flags)
			}
		}
		if col == "" || skip[col] || assigned[col] {
			continue
		}
		t.Errorf("field %s (column %q) is never assigned by Upsert: it would be silently dropped on update",
			f.Name, col)
	}
}
