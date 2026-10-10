// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
)

// newBranchedRepo builds a repository with a commit on main and a later one on dev, returning its path
// (ending in .git, as the clone URL is normalized) and both heads.
func newBranchedRepo(t *testing.T) (dir, mainHead, devHead string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "app.git")
	repo, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.Main},
	})
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	commit := func(msg string) string {
		if err := os.WriteFile(filepath.Join(dir, "README"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add("README"); err != nil {
			t.Fatal(err)
		}
		h, err := wt.Commit(msg, &gogit.CommitOptions{
			Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Unix(1700000000, 0)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return h.String()
	}
	mainHead = commit("main")
	if err := wt.Checkout(&gogit.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("dev"), Create: true}); err != nil {
		t.Fatal(err)
	}
	devHead = commit("dev")
	return dir, mainHead, devHead
}

func pinTestService(t *testing.T) (*Service, func(id uint, sourceType models.AppSourceType, repo, ref string)) {
	t.Helper()
	s, db := scopeTestService(t)
	for _, col := range []string{"source_type text", "git_repo text", "git_ref text", "git_repository_id integer"} {
		if err := db.Exec("ALTER TABLE applications ADD COLUMN " + col).Error; err != nil {
			t.Fatal(err)
		}
	}
	s.SetGitRepos(gitrepo.NewService(nil))
	insert := func(id uint, sourceType models.AppSourceType, repo, ref string) {
		if err := db.Exec("INSERT INTO applications (id, workspace_id, name, source_type, git_repo, git_ref) VALUES (?, 1, ?, ?, ?, ?)",
			id, "app", sourceType, repo, ref).Error; err != nil {
			t.Fatal(err)
		}
	}
	return s, insert
}

func TestPinCommitResolvesTheAppsTrackedBranch(t *testing.T) {
	dir, _, devHead := newBranchedRepo(t)
	s, insert := pinTestService(t)
	insert(1, models.AppSourceGit, dir, "dev")
	app := uint(1)

	in := TriggerInput{Trigger: "manual"}
	s.pinCommit(&models.PipelineDefinition{WorkspaceID: 1, Name: "p", ApplicationID: &app}, &in)
	if in.Branch != "dev" || in.Commit != devHead {
		t.Fatalf("pinned (%q, %q), want (dev, %s)", in.Branch, in.Commit, devHead)
	}
}

func TestPinCommitHonoursTheRequestedBranch(t *testing.T) {
	dir, mainHead, _ := newBranchedRepo(t)
	s, insert := pinTestService(t)
	insert(1, models.AppSourceGit, dir, "dev")
	app := uint(1)

	in := TriggerInput{Trigger: "manual", Branch: "main"}
	s.pinCommit(&models.PipelineDefinition{WorkspaceID: 1, Name: "p", ApplicationID: &app}, &in)
	if in.Branch != "main" || in.Commit != mainHead {
		t.Fatalf("pinned (%q, %q), want (main, %s)", in.Branch, in.Commit, mainHead)
	}
}

func TestPinCommitLeavesTheRunUnpinnedWhenTheRefIsUnknown(t *testing.T) {
	dir, _, _ := newBranchedRepo(t)
	s, insert := pinTestService(t)
	insert(1, models.AppSourceGit, dir, "gone")
	app := uint(1)

	in := TriggerInput{Trigger: "schedule"}
	s.pinCommit(&models.PipelineDefinition{WorkspaceID: 1, Name: "p", ApplicationID: &app}, &in)
	if in.Branch != "gone" || in.Commit != "" {
		t.Fatalf("pinned (%q, %q), want (gone, \"\")", in.Branch, in.Commit)
	}
}

func TestPinCommitSkipsAnImageApp(t *testing.T) {
	s, insert := pinTestService(t)
	insert(1, models.AppSourceImage, "", "")
	app := uint(1)

	in := TriggerInput{Trigger: "manual"}
	s.pinCommit(&models.PipelineDefinition{WorkspaceID: 1, Name: "p", ApplicationID: &app}, &in)
	if in.Branch != "" || in.Commit != "" {
		t.Fatalf("pinned (%q, %q), want nothing", in.Branch, in.Commit)
	}
}
