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
	"strings"
	"sync"
)

// Per request, so one call can't hold the registry for minutes; a rule picks at most this many too.
const maxDeleteTags = 500

var (
	ErrNoTags      = errors.New("registry: no tags given")
	ErrTooManyTags = errors.New("registry: too many tags in one request")
)

// Tag outcomes; a dry run reports would_delete where a delete reports deleted.
const (
	TagDeleted     = "deleted"
	TagWouldDelete = "would_delete"
	TagInUse       = "in_use"
	TagNotFound    = "not_found"
	TagFailed      = "failed"
)

type TagOutcome struct {
	Tag    string `json:"tag"`
	Result string `json:"result"`
	// Siblings point at the same image, so the registry removes them with this tag.
	Siblings []string `json:"siblings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type DeleteResult struct {
	DryRun bool         `json:"dry_run"`
	Tags   []TagOutcome `json:"tags"`
	// Removed is every tag gone afterwards, siblings included; for a dry run, every tag that would go.
	Removed []string `json:"removed"`
	// FreedBytes is what no image left in the workspace still uses; disk shrinks at the next garbage collection.
	FreedBytes int64 `json:"freed_bytes"`
}

// DeleteTags deletes each requested tag's image once; a registry deletes by digest, so every tag on it goes too.
func (s *Service) DeleteTags(ctx context.Context, workspaceID uint, image string, tags []string, dryRun bool) (*DeleteResult, error) {
	repo, err := repoPath(workspaceID, image)
	if err != nil {
		return nil, err
	}
	requested, err := uniqueTags(tags)
	if err != nil {
		return nil, err
	}
	all, err := s.reg.Tags(ctx, repo)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	digests, failures := s.resolveDigests(ctx, repo, all)

	// Can't prove an image is free without it, so refuse rather than risk deleting a live one.
	protected, err := s.protectedIn(ctx, repo, digests)
	if err != nil {
		return nil, err
	}

	byDigest := map[string][]string{}
	for _, t := range all {
		if d := digests[t]; d != "" {
			byDigest[d] = append(byDigest[d], t)
		}
	}
	for d := range byDigest {
		SortTags(byDigest[d])
	}

	res := &DeleteResult{DryRun: dryRun, Tags: make([]TagOutcome, 0, len(requested)), Removed: []string{}}
	var order []string
	queued := map[string]bool{}
	for _, t := range requested {
		o := TagOutcome{Tag: t}
		switch d := digests[t]; {
		case failures[t] != nil:
			o.Result, o.Error = TagFailed, failures[t].Error()
		case d == "":
			o.Result = TagNotFound
		default:
			o.Siblings = siblingsOf(byDigest[d], t, requested)
			if !queued[d] {
				queued[d] = true
				order = append(order, d)
			}
		}
		res.Tags = append(res.Tags, o)
	}

	var deletable []string
	for _, d := range order {
		if !protected[d] {
			deletable = append(deletable, d)
		}
	}
	res.FreedBytes = s.freedBytes(ctx, workspaceID, strings.TrimPrefix(repo, Namespace(workspaceID)+"/"), repo, deletable)

	results := map[string]string{}
	errs := map[string]string{}
	for i, d := range order {
		switch {
		case protected[d]:
			results[d] = TagInUse
		case dryRun:
			results[d] = TagWouldDelete
		default:
			err := s.reg.DeleteManifest(ctx, repo, d)
			switch {
			case err == nil:
				results[d] = TagDeleted
			case errors.Is(err, ErrNotFound):
				results[d] = TagNotFound
			case errors.Is(err, ErrDeleteDisabled) && i == 0:
				// Nothing was deleted yet, so this is the platform's answer for the whole request.
				return nil, err
			default:
				results[d], errs[d] = TagFailed, err.Error()
			}
		}
		if r := results[d]; r == TagDeleted || r == TagWouldDelete {
			res.Removed = append(res.Removed, byDigest[d]...)
		}
	}
	for i := range res.Tags {
		if d := digests[res.Tags[i].Tag]; d != "" {
			res.Tags[i].Result, res.Tags[i].Error = results[d], errs[d]
		}
	}
	SortTags(res.Removed)
	if !dryRun && len(res.Removed) > 0 {
		s.forgetUsage(workspaceID)
	}
	return res, nil
}

func uniqueTags(tags []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if err := validateTag(t); err != nil {
			return nil, err
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	switch {
	case len(out) == 0:
		return nil, ErrNoTags
	case len(out) > maxDeleteTags:
		return nil, ErrTooManyTags
	}
	return out, nil
}

// A sibling is a tag on the same image the caller did not name: the one they might not expect to lose.
func siblingsOf(onDigest []string, tag string, requested []string) []string {
	named := map[string]bool{}
	for _, t := range requested {
		named[t] = true
	}
	var out []string
	for _, t := range onDigest {
		if t != tag && !named[t] {
			out = append(out, t)
		}
	}
	return out
}

// A tag that is gone is left out of both maps; any other error is a failure, never mistaken for not found.
func (s *Service) resolveDigests(ctx context.Context, repo string, tags []string) (map[string]string, map[string]error) {
	digests := make(map[string]string, len(tags))
	failures := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, manifestFetchConcurrency)
	for _, t := range tags {
		wg.Add(1)
		sem <- struct{}{}
		go func(tag string) {
			defer func() { <-sem; wg.Done() }()
			d, err := s.reg.ManifestDigest(ctx, repo, tag)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				digests[tag] = d
			case !errors.Is(err, ErrNotFound):
				failures[tag] = err
			}
		}(t)
	}
	wg.Wait()
	return digests, failures
}

// protectedIn is the digests in repo a live or pinned release uses, whether it names the digest or only a tag.
func (s *Service) protectedIn(ctx context.Context, repo string, digestOf map[string]string) (map[string]bool, error) {
	out := map[string]bool{}
	if s.catalog == nil {
		return out, nil
	}
	digests, err := s.catalog.ProtectedDigests()
	if err != nil {
		return nil, err
	}
	refs, err := s.catalog.ProtectedRefs()
	if err != nil {
		return nil, err
	}
	for d := range digests {
		out[d] = true
	}
	for ref := range refs {
		path, tag, digest := splitImageRef(ref)
		if path != repo {
			continue
		}
		if digest != "" {
			out[digest] = true
			continue
		}
		d := digestOf[tag]
		if d == "" && tag != "" {
			// The release's tag may be off the page or unresolved; a lookup error must not read as unused.
			if d, err = s.reg.ManifestDigest(ctx, repo, tag); err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
		}
		if d != "" {
			out[d] = true
		}
	}
	return out, nil
}

// splitImageRef drops the registry host, which releases record as whatever address they pulled from.
func splitImageRef(ref string) (path, tag, digest string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref, digest = ref[:i], ref[i+1:]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref, tag = ref[:i], ref[i+1:]
	}
	if first, rest, ok := strings.Cut(ref, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		ref = rest
	}
	if tag == "" && digest == "" {
		tag = "latest"
	}
	return ref, tag, digest
}

// freedBytes is an estimate from the usage index taken before the delete; images pushed since are read directly.
func (s *Service) freedBytes(ctx context.Context, workspaceID uint, image, repo string, digests []string) int64 {
	if len(digests) == 0 {
		return 0
	}
	idx := s.peekIndex(workspaceID)
	if idx == nil {
		var err error
		if idx, err = s.rebuildIndex(ctx, workspaceID); err != nil {
			return 0
		}
	}
	view := &usageIndex{tags: idx.tags, blobs: make(map[string]map[string]int64, len(idx.blobs)+len(digests))}
	for d, b := range idx.blobs {
		view.blobs[d] = b
	}
	set := make(map[string]bool, len(digests))
	for _, d := range digests {
		set[d] = true
		if _, ok := view.blobs[d]; !ok {
			if sizes, err := s.reg.BlobSizes(ctx, repo, d); err == nil {
				view.blobs[d] = sizes
			}
		}
	}
	return view.freedBy(image, set)
}
