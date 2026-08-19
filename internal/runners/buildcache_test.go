// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"strings"
	"testing"
)

func TestCacheRefs(t *testing.T) {
	const repo = "reg.example.com/ws_42/web"

	// A trunk build reads and writes one ref: there is no second cache to consult.
	from, to := CacheRefs(repo, "main", "main", 3)
	if to != repo+":cache-main-g3" {
		t.Errorf("export ref = %q", to)
	}
	if len(from) != 1 || from[0] != to {
		t.Errorf("trunk build should import only its own ref, got %v", from)
	}

	// A branch build reads its own cache first, then the trunk's — but writes only its own, so it
	// can never seed layers into the cache a trunk build trusts.
	from, to = CacheRefs(repo, "feat/thing", "main", 3)
	if to != repo+":cache-feat-thing-g3" {
		t.Errorf("export ref = %q, want the branch's own", to)
	}
	if len(from) != 2 || from[0] != to || from[1] != repo+":cache-main-g3" {
		t.Errorf("import refs = %v, want [own, trunk]", from)
	}

	// A bump changes every ref, which is what makes the next build cold.
	if _, next := CacheRefs(repo, "main", "main", 4); next == to {
		t.Error("bumping the generation must change the ref")
	}

	// Nothing to push to means nothing to cache.
	if from, to := CacheRefs("", "main", "main", 1); from != nil || to != "" {
		t.Errorf("no repository should disable the cache, got %v / %q", from, to)
	}
}

// A branch name is far more permissive than a Docker tag, and an illegal tag fails the build at
// push time rather than at the point the name was chosen.
func TestCacheTagIsAValidDockerTag(t *testing.T) {
	const legal = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
	for _, branch := range []string{"main", "feat/thing", "release/v1.2", "dependabot/npm_and_yarn/x@1", "", strings.Repeat("x", 200)} {
		tag := cacheTag(branch, 7)
		if len(tag) > 128 {
			t.Errorf("tag for %q is %d chars, over the 128 limit", branch, len(tag))
		}
		if !strings.HasPrefix(tag, "cache-") {
			t.Errorf("tag %q lost its prefix", tag)
		}
		for _, r := range tag {
			if !strings.ContainsRune(legal, r) {
				t.Errorf("tag %q for branch %q carries illegal rune %q", tag, branch, r)
			}
		}
	}
}
