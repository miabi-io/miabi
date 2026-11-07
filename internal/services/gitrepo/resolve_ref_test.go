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
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func originRepo(t *testing.T, defaultBranch string) (string, map[string]plumbing.Hash) {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName(defaultBranch)},
	})
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	commit := func(msg string) plumbing.Hash {
		h, err := wt.Commit(msg, &gogit.CommitOptions{AllowEmptyCommits: true,
			Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	refs := map[string]plumbing.Hash{}
	refs[defaultBranch] = commit("init")
	if _, err := repo.CreateTag("v1.0.0", refs[defaultBranch], nil); err != nil {
		t.Fatal(err)
	}
	refs["v1.0.0"] = refs[defaultBranch]

	for _, branch := range []string{"develop", "main"} {
		if branch == defaultBranch {
			continue
		}
		name := plumbing.NewBranchReferenceName(branch)
		if err := wt.Checkout(&gogit.CheckoutOptions{Branch: name, Create: true, Hash: refs[defaultBranch]}); err != nil {
			t.Fatal(err)
		}
		refs[branch] = commit("on " + branch)
		if err := wt.Checkout(&gogit.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(defaultBranch)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch)))
	return dir, refs
}

func clone(t *testing.T, url string) *gogit.Repository {
	t.Helper()
	repo, err := gogit.PlainClone(t.TempDir(), false, &gogit.CloneOptions{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// Issue #451: a non-default branch failed with "reference not found" because a clone has it only as origin/<name>.
func TestResolveRefFindsEveryBranch(t *testing.T) {
	url, want := originRepo(t, "main")
	repo := clone(t, url)
	for _, ref := range []string{"main", "develop", "refs/heads/develop", "v1.0.0", want["develop"].String()} {
		got, err := ResolveRef(repo, ref)
		if err != nil {
			t.Errorf("%s: %v", ref, err)
			continue
		}
		exp := want[ref]
		if ref == "refs/heads/develop" || ref == want["develop"].String() {
			exp = want["develop"]
		}
		if *got != exp {
			t.Errorf("%s resolved to %s, want %s", ref, got, exp)
		}
	}
	if _, err := ResolveRef(repo, "no-such-branch"); err == nil {
		t.Error("an unknown ref must fail")
	}
}

// On a master-default repo, "main" names a different branch and must resolve to it.
func TestResolveRefMainOnMasterRepo(t *testing.T) {
	url, want := originRepo(t, "master")
	got, err := ResolveRef(clone(t, url), "main")
	if err != nil {
		t.Fatal(err)
	}
	if *got != want["main"] || *got == want["master"] {
		t.Errorf("main resolved to %s, want %s", got, want["main"])
	}
}
