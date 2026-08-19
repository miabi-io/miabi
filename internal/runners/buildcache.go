// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"fmt"
	"strings"
)

// maxCacheBranchLen caps the branch segment of a cache tag. A Docker tag is 128 characters and the
// rest of the tag is fixed, so a long branch name is truncated rather than rejected.
const maxCacheBranchLen = 64

// CacheRefs derives the registry refs a build reads its layer cache from and writes it to.
//
// The export ref is always the building branch's own, and gen names the current cache generation:
// bumping it points every build at a ref that does not exist yet, which is a cold build now and a
// warm one after. A branch build additionally READS the trunk's cache but never writes to it — a
// pipeline is editable by anyone who can push a branch, so a shared export would let a branch
// build seed layers into the cache a trunk build trusts.
//
// An empty repository (nothing to push to) disables the cache entirely.
func CacheRefs(repository, branch, trunk string, gen uint) (from []string, to string) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return nil, ""
	}
	to = repository + ":" + cacheTag(branch, gen)
	from = []string{to}
	if t := repository + ":" + cacheTag(trunk, gen); t != to {
		from = append(from, t)
	}
	return from, to
}

// cacheTag renders the branch-scoped tag of a cache ref. The "cache-" prefix keeps it a valid tag
// whatever the branch starts with, and separates it from the image tags in the same repository.
func cacheTag(branch string, gen uint) string {
	b := sanitizeTagPart(branch)
	if b == "" {
		return fmt.Sprintf("cache-g%d", gen)
	}
	return fmt.Sprintf("cache-%s-g%d", b, gen)
}

// sanitizeTagPart maps a branch name onto the characters a Docker tag allows; "feat/x" is a legal
// branch and an illegal tag, so the slash (and anything else outside the set) becomes a dash.
func sanitizeTagPart(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxCacheBranchLen {
		s = s[:maxCacheBranchLen]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
