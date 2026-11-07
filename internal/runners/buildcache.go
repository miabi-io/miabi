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

package runners

import (
	"fmt"
	"strings"
)

// Docker tags max out at 128 chars, so long branch names are truncated rather than rejected.
const maxCacheBranchLen = 64

// Branch builds read but never write the trunk cache, since anyone who can push a branch could poison it.
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

// The "cache-" prefix keeps the tag valid whatever the branch starts with and apart from image tags.
func cacheTag(branch string, gen uint) string {
	b := sanitizeTagPart(branch)
	if b == "" {
		return fmt.Sprintf("cache-g%d", gen)
	}
	return fmt.Sprintf("cache-%s-g%d", b, gen)
}

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
