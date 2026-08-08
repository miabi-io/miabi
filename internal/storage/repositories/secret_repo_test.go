// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// secretRow is a sqlite-friendly stand-in for models.Secret: the full model carries
// Postgres-specific defaults sqlite can't migrate, and the paged listing only reads these columns.
type secretRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	Name        string
	Description string
	Managed     bool
}

func (secretRow) TableName() string { return "secrets" }

func newSecretDB(t *testing.T, rows []secretRow) *SecretRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&secretRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return NewSecretRepository(db)
}

func names(t *testing.T, repo *SecretRepository, search string, managed *bool) ([]string, int64) {
	t.Helper()
	got, total, err := repo.ListByWorkspacePaged(1, search, managed, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := make([]string, 0, len(got))
	for i := range got {
		out = append(out, got[i].Name)
	}
	return out, total
}

func TestListByWorkspacePagedFiltersOwnership(t *testing.T) {
	repo := newSecretDB(t, []secretRow{
		{ID: 1, WorkspaceID: 1, Name: "APP_KEY"},
		{ID: 2, WorkspaceID: 1, Name: "DB_SHOP_PASSWORD", Managed: true},
		{ID: 3, WorkspaceID: 1, Name: "GHCR_TOKEN", Description: "registry token"},
		{ID: 4, WorkspaceID: 2, Name: "OTHER_WORKSPACE"},
	})

	yes, no := true, false

	// No filter returns both kinds — and never another workspace's secrets.
	got, total := names(t, repo, "", nil)
	if len(got) != 3 || total != 3 {
		t.Errorf("unfiltered = %v (total %d), want the workspace's 3 secrets", got, total)
	}

	if got, total = names(t, repo, "", &yes); len(got) != 1 || got[0] != "DB_SHOP_PASSWORD" || total != 1 {
		t.Errorf("managed = %v (total %d), want [DB_SHOP_PASSWORD]", got, total)
	}
	if got, total = names(t, repo, "", &no); len(got) != 2 || total != 2 {
		t.Errorf("unmanaged = %v (total %d), want the 2 hand-created secrets", got, total)
	}

	// The count must reflect the filter too — it drives pagination, so a total
	// that ignored the filter would page into rows that aren't there.
	if _, total = names(t, repo, "", &yes); total != 1 {
		t.Errorf("filtered total = %d, want 1", total)
	}

	// Search and ownership compose, and search still matches the description.
	if got, _ = names(t, repo, "token", &no); len(got) != 1 || got[0] != "GHCR_TOKEN" {
		t.Errorf("search+ownership = %v, want [GHCR_TOKEN]", got)
	}
	if got, _ = names(t, repo, "token", &yes); len(got) != 0 {
		t.Errorf("search+ownership = %v, want no matches", got)
	}
}
