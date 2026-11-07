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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// Tag-preview and enrichment bounds.
const (
	// The list exists to find a repository, not read its history.
	DefaultTagPreview = 4
	// Caps requests so the list can't become the unbounded response it replaced.
	maxTagPreview = 25
	// One request per tag is fine for a page; all at once is not.
	manifestFetchConcurrency = 8
)

// RepositorySummary is a repository as it appears in the browse list.
type RepositorySummary struct {
	Name string `json:"name"`
	// Tags carries only the newest DefaultTagPreview; TagCount is the full count.
	TagCount int      `json:"tag_count"`
	Tags     []string `json:"tags"`
}

// Provenance fields are absent for images pushed by hand rather than built by a pipeline.
type TagInfo struct {
	Name      string `json:"name"`
	Digest    string `json:"digest,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	// Deleting an in-use tag is refused, so the UI can say so up front.
	InUse         bool       `json:"in_use"`
	BuiltAt       *time.Time `json:"built_at,omitempty"`
	Commit        string     `json:"commit,omitempty"`
	ApplicationID *uint      `json:"application_id,omitempty"`
	PipelineRunID *uint      `json:"pipeline_run_id,omitempty"`
}

// No total size: that is one manifest read per tag; the newest tag's size is cheap and what people mean.
type RepositoryOverview struct {
	Name      string   `json:"name"`
	TagCount  int      `json:"tag_count"`
	Tags      []string `json:"tags"` // preview, display order
	LatestTag *TagInfo `json:"latest_tag,omitempty"`
}

func (s *Service) repoNames(ctx context.Context, workspaceID uint) ([]string, error) {
	prefix := Namespace(workspaceID) + "/"
	all, err := s.reg.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, repo := range all {
		if strings.HasPrefix(repo, prefix) {
			out = append(out, strings.TrimPrefix(repo, prefix))
		}
	}
	sort.Strings(out)
	return out, nil
}

// ListRepositoriesPage fetches tags only for the page's repositories, avoiding a round trip per repository.
func (s *Service) ListRepositoriesPage(ctx context.Context, workspaceID uint, q string, offset, limit, tagPreview int) ([]RepositorySummary, int, error) {
	names, err := s.repoNames(ctx, workspaceID)
	if err != nil {
		return nil, 0, err
	}
	if q = strings.TrimSpace(strings.ToLower(q)); q != "" {
		filtered := names[:0:0]
		for _, n := range names {
			if strings.Contains(strings.ToLower(n), q) {
				filtered = append(filtered, n)
			}
		}
		names = filtered
	}
	total := len(names)
	if tagPreview <= 0 || tagPreview > maxTagPreview {
		tagPreview = DefaultTagPreview
	}
	if offset >= total {
		return nil, total, nil
	}
	end := min(offset+limit, total)
	page := names[offset:end]

	prefix := Namespace(workspaceID) + "/"
	out := make([]RepositorySummary, len(page))
	for i, name := range page {
		tags, err := s.reg.Tags(ctx, prefix+name)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, 0, err
		}
		SortTags(tags)
		out[i] = RepositorySummary{
			Name:     name,
			TagCount: len(tags),
			Tags:     nonNil(tags[:min(tagPreview, len(tags))]),
		}
	}
	return out, total, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Service) Overview(ctx context.Context, workspaceID uint, image string) (*RepositoryOverview, error) {
	repo, err := repoPath(workspaceID, image)
	if err != nil {
		return nil, err
	}
	tags, err := s.reg.Tags(ctx, repo)
	if err != nil {
		return nil, err
	}
	SortTags(tags)
	ov := &RepositoryOverview{
		Name:     image,
		TagCount: len(tags),
		Tags:     nonNil(tags[:min(DefaultTagPreview, len(tags))]),
	}
	if len(tags) > 0 {
		enriched, err := s.enrichTags(ctx, workspaceID, repo, tags[:1])
		if err != nil {
			return nil, err
		}
		if len(enriched) > 0 {
			ov.LatestTag = &enriched[0]
		}
	}
	return ov, nil
}

func (s *Service) ListTagsPage(ctx context.Context, workspaceID uint, image, q string, offset, limit int) ([]TagInfo, int, error) {
	repo, err := repoPath(workspaceID, image)
	if err != nil {
		return nil, 0, err
	}
	tags, err := s.reg.Tags(ctx, repo)
	if err != nil {
		return nil, 0, err
	}
	SortTags(tags)
	if q = strings.TrimSpace(strings.ToLower(q)); q != "" {
		filtered := tags[:0:0]
		for _, t := range tags {
			if strings.Contains(strings.ToLower(t), q) {
				filtered = append(filtered, t)
			}
		}
		tags = filtered
	}
	total := len(tags)
	if offset >= total {
		return nil, total, nil
	}
	page := tags[offset:min(offset+limit, total)]
	enriched, err := s.enrichTags(ctx, workspaceID, repo, page)
	if err != nil {
		return nil, 0, err
	}
	return enriched, total, nil
}

// One registry request per tag, hence page-only; an unreadable manifest still returns the tag, bare.
func (s *Service) enrichTags(ctx context.Context, workspaceID uint, repo string, tags []string) ([]TagInfo, error) {
	out := make([]TagInfo, len(tags))
	var wg sync.WaitGroup
	sem := make(chan struct{}, manifestFetchConcurrency)
	for i, tag := range tags {
		out[i] = TagInfo{Name: tag}
		wg.Add(1)
		go func(i int, tag string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			digest, size, err := s.reg.ManifestInfo(ctx, repo, tag)
			if err != nil {
				return
			}
			out[i].Digest, out[i].SizeBytes = digest, size
		}(i, tag)
	}
	wg.Wait()

	if s.catalog == nil {
		return out, nil
	}
	digests := make([]string, 0, len(out))
	for _, t := range out {
		if t.Digest != "" {
			digests = append(digests, t.Digest)
		}
	}
	if len(digests) == 0 {
		return out, nil
	}
	// Best-effort: deletion is guarded server-side, so a missing in-use mark can't lose data.
	protected, _ := s.catalog.ProtectedDigests()
	rows, _ := s.catalog.ByDigests(workspaceID, digests)
	for i := range out {
		d := out[i].Digest
		if d == "" {
			continue
		}
		out[i].InUse = protected[d]
		if img, ok := rows[d]; ok {
			out[i].Commit = img.Commit
			out[i].BuiltAt = img.BuiltAt
			out[i].ApplicationID = img.ApplicationID
			out[i].PipelineRunID = img.PipelineRunID
			if out[i].SizeBytes == 0 {
				out[i].SizeBytes = img.SizeBytes
			}
		}
	}
	return out, nil
}

// Catalog is nil-safe; without it tags list unannotated and deletion loses its in-use guard.
type Catalog interface {
	ProtectedDigests() (map[string]bool, error)
	ByDigests(workspaceID uint, digests []string) (map[string]models.Image, error)
}
