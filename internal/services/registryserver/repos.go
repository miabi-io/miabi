// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	ErrNotFound = errors.New("registry: not found")
	// ErrDeleteDisabled is returned when the registry's delete API is off
	// (REGISTRY_STORAGE_DELETE_ENABLED=false / the DeleteEnabled setting).
	ErrDeleteDisabled = errors.New("registry: delete is not enabled")
	// ErrTagInUse is returned when the tag resolves to an image a live deployment or a pinned release still
	// holds. Deleting it would leave that release unpullable, and the failure would surface later, on a node,
	// as a mystery.
	ErrTagInUse = errors.New("registry: this image is used by a live deployment or a pinned release")
)

// SetCatalog wires the image catalog: in-use protection for tag deletion, and
// build provenance on tag listings. See Catalog in browse.go.
func (s *Service) SetCatalog(c Catalog) { s.catalog = c }

// Repository is a workspace repository and its tags, with the immutable ws_<id>
// storage namespace stripped so callers see the user-facing image name.
type Repository struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// ListRepositories returns the workspace's repositories (its ws_<id> namespace,
// filtered from the registry catalog) with their tags.
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

// SortTags orders a repository's tags for display: "latest" first, then newest version first. Plain
// lexicographic order puts v10 before v9 and buries "latest" in the middle. Runs of digits are compared
// numerically and reversed; tags with no digits end up reverse-alphabetical, which is arbitrary but stable.
func SortTags(tags []string) {
	sort.Slice(tags, func(i, j int) bool {
		a, b := tags[i], tags[j]
		if (a == "latest") != (b == "latest") {
			return a == "latest"
		}
		return naturalLess(b, a) // reversed: newest first
	})
}

// naturalLess compares two strings treating runs of digits as numbers, so
// "v9" < "v10". Equal-valued numeric runs fall back to the literal text, which
// keeps zero-padded variants ("v01" vs "v1") in a stable order.
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

// DeleteTag deletes a tag from a workspace repository by resolving it to its manifest digest and deleting
// the manifest (the registry has no delete-by-tag). image is the user-facing name. A tag a live deployment
// or pinned release still holds is refused with ErrTagInUse, since the breakage would surface on a node.
func (s *Service) DeleteTag(ctx context.Context, workspaceID uint, image, tag string) error {
	repo := Namespace(workspaceID) + "/" + strings.Trim(image, "/")
	digest, err := s.reg.ManifestDigest(ctx, repo, tag)
	if err != nil {
		return err
	}
	if s.catalog != nil {
		protected, perr := s.catalog.ProtectedDigests()
		if perr != nil {
			// Can't prove the image is free — refuse rather than risk deleting one
			// that's live.
			return fmt.Errorf("check whether the image is in use: %w", perr)
		}
		if protected[digest] {
			return ErrTagInUse
		}
	}
	return s.reg.DeleteManifest(ctx, repo, digest)
}
