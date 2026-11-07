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

// The path ends in .git, as the clone URL is normalized.
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
