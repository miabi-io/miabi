// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package gitrepo

import (
	"context"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestMatchRemoteRef(t *testing.T) {
	const (
		mainSHA    = "1111111111111111111111111111111111111111"
		devSHA     = "2222222222222222222222222222222222222222"
		tagObject  = "3333333333333333333333333333333333333333"
		tagCommit  = "4444444444444444444444444444444444444444"
		lightTagSH = "5555555555555555555555555555555555555555"
	)
	refs := []*plumbing.Reference{
		plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main"),
		plumbing.NewHashReference("refs/heads/main", plumbing.NewHash(mainSHA)),
		plumbing.NewHashReference("refs/heads/dev", plumbing.NewHash(devSHA)),
		plumbing.NewHashReference("refs/tags/v1.0.0", plumbing.NewHash(tagObject)),
		plumbing.NewHashReference("refs/tags/v1.0.0^{}", plumbing.NewHash(tagCommit)),
		plumbing.NewHashReference("refs/tags/light", plumbing.NewHash(lightTagSH)),
	}
	cases := []struct {
		ref  string
		want string
		ok   bool
	}{
		{"", mainSHA, true},
		{"main", mainSHA, true},
		{"dev", devSHA, true},
		{"refs/heads/dev", devSHA, true},
		{"v1.0.0", tagCommit, true},
		{"refs/tags/v1.0.0", tagCommit, true},
		{"light", lightTagSH, true},
		{"missing", "", false},
	}
	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			got, ok := matchRemoteRef(refs, c.ref)
			if got != c.want || ok != c.ok {
				t.Errorf("matchRemoteRef(%q) = (%q, %v), want (%q, %v)", c.ref, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestRemoteCommitReturnsAFullHashWithoutListing(t *testing.T) {
	const sha = "1111111111111111111111111111111111111111"
	got, err := RemoteCommit(context.Background(), "https://invalid.example/repo.git", sha, nil)
	if err != nil || got != sha {
		t.Fatalf("RemoteCommit = (%q, %v), want (%q, nil)", got, err, sha)
	}
}
