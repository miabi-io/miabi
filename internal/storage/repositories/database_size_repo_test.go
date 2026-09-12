// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func sizesDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sizes.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.DatabaseSize{}, &models.Plan{}, &models.WorkspaceQuota{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// A size still offered by a plan or a workspace override must be found, or deleting it would strand them.
func TestDatabaseSizeOfferedBy(t *testing.T) {
	db := sizesDB(t)
	repo := NewDatabaseSizeRepository(db)
	medium := &models.DatabaseSize{Name: "medium", MemoryBytes: 2 << 30, NanoCPUs: 1_000_000_000}
	small := &models.DatabaseSize{Name: "small", MemoryBytes: 1 << 29, NanoCPUs: 500_000_000}
	for _, s := range []*models.DatabaseSize{medium, small} {
		if err := repo.Create(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.Plan{Name: "Pro", DatabaseSizes: []uint{medium.ID, small.ID}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Plan{Name: "Free"}).Error; err != nil {
		t.Fatal(err)
	}
	onlySmall := []uint{small.ID}
	if err := db.Create(&models.WorkspaceQuota{WorkspaceID: 7, DatabaseSizes: &onlySmall}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.WorkspaceQuota{WorkspaceID: 8}).Error; err != nil {
		t.Fatal(err)
	}

	plans, workspaces, err := repo.OfferedBy(small.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plans, []string{"Pro"}) || !slices.Equal(workspaces, []uint{7}) {
		t.Errorf("small offered by plans %v, workspaces %v; want [Pro] and [7]", plans, workspaces)
	}
	if plans, workspaces, _ := repo.OfferedBy(medium.ID); !slices.Equal(plans, []string{"Pro"}) || len(workspaces) != 0 {
		t.Errorf("medium offered by plans %v, workspaces %v; want [Pro] and none", plans, workspaces)
	}

	var inherit models.WorkspaceQuota
	if err := db.First(&inherit, "workspace_id = ?", 8).Error; err != nil {
		t.Fatal(err)
	}
	if inherit.DatabaseSizes != nil {
		t.Errorf("an override that never set sizes must still inherit the plan, got %v", *inherit.DatabaseSizes)
	}
	if list, _ := repo.List(); len(list) != 2 || list[0].Name != "small" {
		t.Errorf("list = %+v, want the smallest size first", list)
	}
}
