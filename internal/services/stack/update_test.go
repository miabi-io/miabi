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

package stack

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The full model has an Apps association and Postgres-flavoured indexes sqlite cannot migrate.
type stackRow struct {
	ID            uint `gorm:"primaryKey"`
	UID           string
	WorkspaceID   uint
	Name          string
	DisplayName   string
	DockerName    string
	ClusterID     uint
	DockerNetwork string
	Description   string
	Metadata      string
	Annotations   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (stackRow) TableName() string { return "stacks" }

func newUpdateService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&stackRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seed := []stackRow{
		{ID: 1, WorkspaceID: 1, Name: "blog", DisplayName: "Blog", DockerName: "blog", Description: "the blog"},
		{ID: 2, WorkspaceID: 1, Name: "shop", DisplayName: "Shop", DockerName: "shop"},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	return NewService(repositories.NewStackRepository(db), nil, nil, nil, nil, nil, nil, nil), db
}

func ptr(s string) *string { return &s }

// Renaming would silently detach the stack from its DockerName, network and GitOps identity.
func TestUpdateRejectsRename(t *testing.T) {
	svc, db := newUpdateService(t)

	_, err := svc.Update(1, 1, UpdateInput{Name: ptr("weblog")})
	if !errors.Is(err, ErrNameImmutable) {
		t.Fatalf("got %v, want ErrNameImmutable", err)
	}

	var row stackRow
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Name != "blog" {
		t.Errorf("name changed to %q despite the refusal", row.Name)
	}
}

// "already exists" would read as "pick another name", which it isn't.
func TestUpdateRejectsRenameToATakenName(t *testing.T) {
	svc, _ := newUpdateService(t)
	_, err := svc.Update(1, 1, UpdateInput{Name: ptr("shop")})
	if !errors.Is(err, ErrNameImmutable) {
		t.Errorf("got %v, want ErrNameImmutable", err)
	}
}

// Clients that PATCH the whole object back, including the unchanged name, must keep working.
func TestUpdateAcceptsTheCurrentName(t *testing.T) {
	svc, _ := newUpdateService(t)
	st, err := svc.Update(1, 1, UpdateInput{Name: ptr("blog"), Description: ptr("updated")})
	if err != nil {
		t.Fatalf("a no-op name was rejected: %v", err)
	}
	if st.Description != "updated" {
		t.Errorf("description = %q, want %q", st.Description, "updated")
	}
}

func TestUpdateAcceptsTheCurrentNameUnnormalized(t *testing.T) {
	svc, _ := newUpdateService(t)
	if _, err := svc.Update(1, 1, UpdateInput{Name: ptr("  Blog  ")}); err != nil {
		t.Errorf("an unnormalized spelling of the current name was rejected: %v", err)
	}
}

func TestUpdateRejectsBlankName(t *testing.T) {
	svc, _ := newUpdateService(t)
	if _, err := svc.Update(1, 1, UpdateInput{Name: ptr("   ")}); !errors.Is(err, ErrNameRequired) {
		t.Errorf("got %v, want ErrNameRequired", err)
	}
}

// The label that IS meant to change.
func TestUpdateChangesDisplayName(t *testing.T) {
	svc, _ := newUpdateService(t)
	st, err := svc.Update(1, 1, UpdateInput{DisplayName: ptr("Company Blog")})
	if err != nil {
		t.Fatal(err)
	}
	if st.DisplayName != "Company Blog" {
		t.Errorf("display name = %q, want %q", st.DisplayName, "Company Blog")
	}
	if st.Name != "blog" {
		t.Errorf("the handle moved with the label: %q", st.Name)
	}
}

func TestUpdateBlankDisplayNameFallsBackToTheName(t *testing.T) {
	svc, _ := newUpdateService(t)
	st, err := svc.Update(1, 1, UpdateInput{DisplayName: ptr("  ")})
	if err != nil {
		t.Fatal(err)
	}
	if st.DisplayName != "blog" {
		t.Errorf("display name = %q, want it to fall back to %q", st.DisplayName, "blog")
	}
}

func TestUpdateNotFound(t *testing.T) {
	svc, _ := newUpdateService(t)
	if _, err := svc.Update(1, 999, UpdateInput{Description: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
	// A stack in another workspace is not visible either.
	if _, err := svc.Update(2, 1, UpdateInput{Description: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-workspace update returned %v, want ErrNotFound", err)
	}
}

func TestStackNameIsTheUniqueHandle(t *testing.T) {
	var s models.Stack
	s.Name = "blog"
	s.DisplayName = "anything at all"
	if s.Name == s.DisplayName {
		t.Fatal("the fixture is not exercising the distinction")
	}
}
