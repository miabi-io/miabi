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

package gitrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type gitRepoRow struct {
	ID                  uint `gorm:"primaryKey"`
	WorkspaceID         uint
	Name                string
	DisplayName         string
	URL                 string
	AuthType            string
	Username            string
	Secret              string
	SecretRef           string
	Metadata            string
	Annotations         string
	ConnectionStatus    string
	ConnectionError     string
	ConnectionCheckedAt *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (gitRepoRow) TableName() string { return "git_repositories" }

func newUpdateService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	crypto.Init("test-master-key-for-gitrepo-update")
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&gitRepoRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seed := []gitRepoRow{
		{ID: 1, WorkspaceID: 1, Name: "acme-api", DisplayName: "Acme API", URL: "https://github.com/acme/api", AuthType: string(models.GitAuthPublic)},
		{ID: 2, WorkspaceID: 1, Name: "acme-web", DisplayName: "Acme Web", URL: "https://github.com/acme/web", AuthType: string(models.GitAuthPublic)},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := NewService(repositories.NewGitRepoRepository(db))
	// A create or credential change probes the remote, and a fixture URL is not somewhere to send a request.
	svc.SetDialer(func(context.Context, *models.GitRepository) error { return nil })
	return svc, db
}

func TestUpdateRejectsRename(t *testing.T) {
	svc, db := newUpdateService(t)

	_, err := svc.Update(1, 1, Input{Name: "acme-backend", URL: "https://github.com/acme/api"})
	if !errors.Is(err, ErrNameImmutable) {
		t.Fatalf("got %v, want ErrNameImmutable", err)
	}

	var row gitRepoRow
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Name != "acme-api" {
		t.Errorf("name changed to %q despite the refusal", row.Name)
	}
}

// "Already exists" would read as "pick another name", which is not the rule.
func TestUpdateRejectsRenameToATakenName(t *testing.T) {
	svc, _ := newUpdateService(t)
	if _, err := svc.Update(1, 1, Input{Name: "acme-web"}); !errors.Is(err, ErrNameImmutable) {
		t.Errorf("got %v, want ErrNameImmutable", err)
	}
}

// Clients that PATCH the whole object back — name included — must keep working.
func TestUpdateAcceptsTheCurrentName(t *testing.T) {
	svc, _ := newUpdateService(t)
	g, err := svc.Update(1, 1, Input{Name: "acme-api", URL: "https://github.com/acme/api-v2"})
	if err != nil {
		t.Fatalf("a no-op name was rejected: %v", err)
	}
	// normalizeGitURL canonicalizes the stored URL (it appends .git).
	if g.URL != "https://github.com/acme/api-v2.git" {
		t.Errorf("url = %q, want the updated one", g.URL)
	}
}

func TestUpdateAcceptsTheCurrentNameUnnormalized(t *testing.T) {
	svc, _ := newUpdateService(t)
	if _, err := svc.Update(1, 1, Input{Name: "  Acme-API  "}); err != nil {
		t.Errorf("an unnormalized spelling of the current name was rejected: %v", err)
	}
}

func TestUpdateWithoutANameIsNotARename(t *testing.T) {
	svc, _ := newUpdateService(t)
	if _, err := svc.Update(1, 1, Input{URL: "https://github.com/acme/api"}); err != nil {
		t.Errorf("a partial update was rejected: %v", err)
	}
}

// The label that IS meant to change.
func TestUpdateChangesDisplayName(t *testing.T) {
	svc, _ := newUpdateService(t)
	g, err := svc.Update(1, 1, Input{DisplayName: "Acme API (production)"})
	if err != nil {
		t.Fatal(err)
	}
	if g.DisplayName != "Acme API (production)" {
		t.Errorf("display name = %q", g.DisplayName)
	}
	if g.Name != "acme-api" {
		t.Errorf("the handle moved with the label: %q", g.Name)
	}
}

func TestUpdateNotFound(t *testing.T) {
	svc, _ := newUpdateService(t)
	if _, err := svc.Update(1, 999, Input{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
	// A repository in another workspace is not visible either.
	if _, err := svc.Update(2, 1, Input{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-workspace update returned %v, want ErrNotFound", err)
	}
}
