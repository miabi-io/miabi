// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// newTestRepo builds a real git repository on disk carrying a Dockerfile and a pipeline document on branch
// main, plus a v1 tag on the same commit. Discover clones it over go-git's local transport, so this
// exercises the actual clone path — not just the fs read.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.Main},
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".miabi"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("Dockerfile", "FROM scratch\n")
	write(".miabi/pipeline.yaml", guestbookPipeline)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatalf("add: %v", err)
	}
	hash, err := wt.Commit("initial", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Unix(1700000000, 0)},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := repo.CreateTag("v1", hash, nil); err != nil {
		t.Fatalf("tag: %v", err)
	}
	// A shallow single-branch clone refuses to serve a repo whose HEAD branch has
	// no upstream config; nothing to configure for a local clone, but keep the
	// remote name conventional so the clone resolves refs/heads/main normally.
	_, _ = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{dir}})
	return dir
}

func TestDiscoverClonesAndReadsPipeline(t *testing.T) {
	src := newTestRepo(t)

	for _, ref := range []string{"", "main", "v1"} {
		t.Run("ref="+ref, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			found, err := Discover(ctx, src, ref, nil)
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			if !found.HasPipeline() {
				t.Fatalf("no pipeline found (path=%q err=%q)", found.Path, found.SpecError)
			}
			if found.Path != ".miabi/pipeline.yaml" {
				t.Errorf("path = %q", found.Path)
			}
			if !found.HasDockerfile {
				t.Error("root Dockerfile not detected")
			}
			if found.Commit == "" {
				t.Error("commit not resolved")
			}
			if found.Raw != guestbookPipeline {
				t.Error("stored spec is not byte-identical to the repo file")
			}
			if len(found.Spec.Steps) != 3 {
				t.Errorf("steps = %d", len(found.Spec.Steps))
			}
		})
	}
}

func TestDiscoverRepoWithoutPipeline(t *testing.T) {
	src := newTestRepo(t)
	if err := os.Remove(filepath.Join(src, ".miabi", "pipeline.yaml")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	repo, err := gogit.PlainOpen(src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wt, _ := repo.Worktree()
	if err := wt.AddGlob("."); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("drop pipeline", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Unix(1700000001, 0)},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	found, err := Discover(ctx, src, "main", nil)
	if err != nil {
		t.Fatalf("a repo without a pipeline must not error: %v", err)
	}
	if found.HasPipeline() || found.Path != "" {
		t.Errorf("unexpected pipeline: %+v", found)
	}
	if !found.HasDockerfile {
		t.Error("root Dockerfile not detected")
	}
}

func TestDiscoverUnreachableRepo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := Discover(ctx, filepath.Join(t.TempDir(), "nope"), "main", nil); err == nil {
		t.Fatal("want an error for an unreachable repository")
	}
}
