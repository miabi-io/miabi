// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// gitRepoRow is a sqlite-friendly stand-in for the git_repositories table.
// Reads and writes still go through the real models.GitRepository schema.
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
	// Never reach the network from a unit test: a create or a credential change
	// probes the remote, and a fixture URL is not somewhere to send a request.
	svc.SetDialer(func(context.Context, *models.GitRepository) error { return nil })
	return svc, db
}

// The name identifies the credential wherever it is referenced — by the apps
// that clone through it, and by pipelines that bind to it as their source — so
// it is an identity, not a label.
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

// A rename onto a name that is merely taken must still report immutability:
// "already exists" would read as "pick another name", which is not the rule.
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

// The name is normalized before comparison, so a differently-cased or spaced
// spelling of the same handle is a no-op rather than a refusal.
func TestUpdateAcceptsTheCurrentNameUnnormalized(t *testing.T) {
	svc, _ := newUpdateService(t)
	if _, err := svc.Update(1, 1, Input{Name: "  Acme-API  "}); err != nil {
		t.Errorf("an unnormalized spelling of the current name was rejected: %v", err)
	}
}

// An omitted name is not a rename attempt — a partial update must not trip the
// refusal.
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
