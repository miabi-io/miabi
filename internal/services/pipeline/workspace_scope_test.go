// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

func TestDiscoverDoesNotFollowSymlinksOutOfTheCheckout(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "environ")
	if err := os.WriteFile(secret, []byte("MIABI_JWT_SECRET=leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	repo, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.Main},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".miabi"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := strings.Repeat("../", 32) + strings.TrimPrefix(secret, "/")
	if err := os.Symlink(target, filepath.Join(dir, ".miabi", "pipeline.yaml")); err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("symlink", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Unix(1700000000, 0)},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	found, err := Discover(ctx, dir, "", nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if strings.Contains(found.Raw, "leaked") || strings.Contains(found.SpecError, "leaked") {
		t.Fatalf("read through symlink: raw=%q err=%q", found.Raw, found.SpecError)
	}
	if found.HasPipeline() {
		t.Fatal("an escaping symlink must not yield a pipeline")
	}
}

func scopeTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scope.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// These models' full schemas use Postgres-only defaults; the tests need only these columns.
	for _, ddl := range []string{
		"CREATE TABLE applications (id integer primary key, workspace_id integer, name text, stack_id integer, deleted_at datetime)",
		"CREATE TABLE app_env_vars (id integer primary key, application_id integer)",
		"CREATE TABLE app_ports (id integer primary key, application_id integer)",
		"CREATE TABLE networks (id integer primary key)",
		"CREATE TABLE application_networks (application_id integer, network_id integer)",
		"CREATE TABLE pipeline_definitions (uid text, id integer primary key, workspace_id integer, name text, display_name text, " +
			"application_id integer, git_repository_id integer, branch text, spec text, enabled numeric, webhook_secret text, " +
			"source text, source_path text, source_ref text, source_commit text, created_at datetime, updated_at datetime)",
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(repositories.NewPipelineRepository(db), nil)
	s.SetApps(repositories.NewApplicationRepository(db))
	return s, db
}

const commandOnlySpec = "apiVersion: miabi.io/v1\nkind: Pipeline\nmetadata:\n  name: p\nsteps:\n  - name: hi\n    image: alpine\n    run: echo hi\n"

func TestCreateRejectsAnotherWorkspacesApplication(t *testing.T) {
	s, db := scopeTestService(t)
	if err := db.Exec("INSERT INTO applications (id, workspace_id, name) VALUES (42, 2, 'victim')").Error; err != nil {
		t.Fatal(err)
	}
	victim := &models.Application{ID: 42}

	_, err := s.Create(1, Input{Name: "steal", Spec: commandOnlySpec, ApplicationID: &victim.ID})
	if !errors.Is(err, ErrApplicationNotFound) {
		t.Fatalf("create bound to a foreign app: err = %v, want ErrApplicationNotFound", err)
	}

	own, err := s.Create(2, Input{Name: "ok", Spec: commandOnlySpec, ApplicationID: &victim.ID})
	if err != nil {
		t.Fatalf("create in the app's own workspace: %v", err)
	}

	mine, err := s.Create(1, Input{Name: "mine", Spec: commandOnlySpec})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(1, mine.ID, Input{ApplicationID: &victim.ID, SetApplicationID: true})
	if !errors.Is(err, ErrApplicationNotFound) {
		t.Fatalf("update to a foreign app: err = %v, want ErrApplicationNotFound", err)
	}
	if own.ApplicationID == nil || *own.ApplicationID != victim.ID {
		t.Fatal("same-workspace binding was not stored")
	}
}

func TestBindingCheckFailsClosedWhenUnwired(t *testing.T) {
	id := uint(7)
	s := &Service{}
	if err := s.checkBindingsInWorkspace(1, &id, nil); !errors.Is(err, ErrApplicationNotFound) {
		t.Fatalf("app: err = %v", err)
	}
	if err := s.checkBindingsInWorkspace(1, nil, &id); !errors.Is(err, ErrRepositoriesUnavailable) {
		t.Fatalf("repo: err = %v", err)
	}
}
