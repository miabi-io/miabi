// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// DefaultTagPreview is how many of a repository's newest tags the browse
	// list carries. The list exists to find a repository, not to read its
	// history — the rest are one click away on the repository page.
	DefaultTagPreview = 4
	// maxTagPreview caps what a client may request, so the list can't be turned
	// back into the unbounded response this replaced.
	maxTagPreview = 25
	// manifestFetchConcurrency bounds the parallel manifest reads used to enrich
	// a page of tags with digests and sizes. One request per tag is fine for a
	// page; doing them all at once is not.
	manifestFetchConcurrency = 8
)

// RepositorySummary is a repository as it appears in the browse list.
type RepositorySummary struct {
	Name string `json:"name"`
	// TagCount is the repository's full tag count; Tags carries only the newest
	// DefaultTagPreview of them, in display order.
	TagCount int      `json:"tag_count"`
	Tags     []string `json:"tags"`
}

// TagInfo is one tag on a repository's tags page, enriched with what the platform knows about it. Digest
// and SizeBytes come from the registry; the provenance fields come from the image catalog and are absent
// for images pushed by hand rather than built by a pipeline.
type TagInfo struct {
	Name      string `json:"name"`
	Digest    string `json:"digest,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	// InUse marks a tag whose image a live deployment or pinned release holds.
	// Deleting it is refused, so the UI can say so before the user tries.
	InUse         bool       `json:"in_use"`
	BuiltAt       *time.Time `json:"built_at,omitempty"`
	Commit        string     `json:"commit,omitempty"`
	ApplicationID *uint      `json:"application_id,omitempty"`
	PipelineRunID *uint      `json:"pipeline_run_id,omitempty"`
}

// RepositoryOverview is the summary shown on a repository's page. It deliberately does not total the
// repository's size: that would mean one manifest read per tag, and a repository accumulating build tags
// has hundreds. The newest tag's size is both cheap and the number people actually mean.
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

// ListRepositoriesPage returns one page of the workspace's repositories, each with its tag count and a
// preview of its newest tags, plus the total matching q. Only the page's repositories have their tags
// fetched — listing every repository with every tag cost one round trip per repository.
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

// Overview returns a repository's summary: its tag count, a preview of the
// newest tags, and the newest tag enriched with digest, size, and provenance.
func (s *Service) Overview(ctx context.Context, workspaceID uint, image string) (*RepositoryOverview, error) {
	repo := Namespace(workspaceID) + "/" + strings.Trim(image, "/")
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

// ListTagsPage returns one page of a repository's tags in display order,
// enriched with digest, size, in-use state, and build provenance, plus the total
// number of tags matching q.
func (s *Service) ListTagsPage(ctx context.Context, workspaceID uint, image, q string, offset, limit int) ([]TagInfo, int, error) {
	repo := Namespace(workspaceID) + "/" + strings.Trim(image, "/")
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

// enrichTags resolves each tag's digest and size from the registry, then joins the digests against the image
// catalog for provenance and the protected set for in-use state. The registry reads are one request per tag,
// which is why only a page is enriched. A tag whose manifest can't be read is still returned, just bare.
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
	// Provenance and in-use state are both best-effort: a catalog read failure
	// leaves the tags un-annotated rather than failing the listing. Deletion is
	// guarded server-side regardless, so a missing in-use mark can't lose data.
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

// Catalog supplies what the platform knows about the images it built: which digests must not be deleted,
// and the build behind a digest. Implemented by image.Service. nil-safe — without it tags still list,
// just unannotated, and tag deletion loses its in-use guard.
type Catalog interface {
	ProtectedDigests() (map[string]bool, error)
	ByDigests(workspaceID uint, digests []string) (map[string]models.Image, error)
}
