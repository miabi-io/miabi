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

package registryserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	// ErrNotFound is returned when a repository, tag, or manifest is absent.
	ErrNotFound       = errors.New("registry: not found")
	ErrDeleteDisabled = errors.New("registry: delete is not enabled")
	// Deleting it would leave that release unpullable, failing later on a node.
	ErrTagInUse = errors.New("registry: this image is used by a live deployment or a pinned release")
)

func (s *Service) SetCatalog(c Catalog) { s.catalog = c }

// The ws_<id> namespace is stripped so callers see the user-facing name.
type Repository struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

func (s *Service) ListRepositories(ctx context.Context, workspaceID uint) ([]Repository, error) {
	prefix := Namespace(workspaceID) + "/"
	all, err := s.reg.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Repository, 0)
	for _, repo := range all {
		if !strings.HasPrefix(repo, prefix) {
			continue
		}
		image := strings.TrimPrefix(repo, prefix)
		tags, err := s.reg.Tags(ctx, repo)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		SortTags(tags)
		out = append(out, Repository{Name: image, Tags: tags})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SortTags puts "latest" first, then newest version; plain lexicographic order puts v10 before v9.
func SortTags(tags []string) {
	sort.Slice(tags, func(i, j int) bool {
		a, b := tags[i], tags[j]
		if (a == "latest") != (b == "latest") {
			return a == "latest"
		}
		return naturalLess(b, a) // reversed: newest first
	})
}

// Equal numeric runs fall back to literal text, keeping "v01" vs "v1" stable.
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		ad, bd := isDigit(a[0]), isDigit(b[0])
		if ad != bd {
			return a < b // one starts with a digit, the other doesn't
		}
		var ar, br string
		ar, a = splitRun(a, ad)
		br, b = splitRun(b, bd)
		if ar == br {
			continue
		}
		if !ad {
			return ar < br
		}
		// Numeric run: compare by value, then by length so "1" precedes "01".
		an, bn := strings.TrimLeft(ar, "0"), strings.TrimLeft(br, "0")
		if len(an) != len(bn) {
			return len(an) < len(bn)
		}
		if an != bn {
			return an < bn
		}
		return len(ar) < len(br)
	}
	return len(a) < len(b)
}

// splitRun peels off the leading run of digits (or of non-digits) from s.
func splitRun(s string, digits bool) (run, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) == digits {
		i++
	}
	return s[:i], s[i:]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// DeleteTag deletes by manifest digest (no delete-by-tag); tags held by live releases get ErrTagInUse.
func (s *Service) DeleteTag(ctx context.Context, workspaceID uint, image, tag string) error {
	repo, err := repoPath(workspaceID, image)
	if err != nil {
		return err
	}
	if err := validateTag(tag); err != nil {
		return err
	}
	digest, err := s.reg.ManifestDigest(ctx, repo, tag)
	if err != nil {
		return err
	}
	if s.catalog != nil {
		protected, perr := s.catalog.ProtectedDigests()
		if perr != nil {
			// Can't prove the image is free, so refuse rather than risk deleting a live one.
			return fmt.Errorf("check whether the image is in use: %w", perr)
		}
		if protected[digest] {
			return ErrTagInUse
		}
	}
	return s.reg.DeleteManifest(ctx, repo, digest)
}
