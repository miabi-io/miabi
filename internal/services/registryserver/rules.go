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
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoRule  = errors.New("registry: give keep_newest, older_than_days, or both")
	ErrBadRule = errors.New("registry: keep_newest and older_than_days must not be negative")
)

// SelectRule picks images to delete; both set means both must hold.
type SelectRule struct {
	KeepNewest    int
	OlderThanDays int
}

type Selection struct {
	// Tags are every tag on the picked images, oldest image first, capped at the request limit.
	Tags   []string `json:"tags"`
	Images int      `json:"images"`
	// Matched counts the tags before the cap; Truncated says a second run will find more.
	Matched   int  `json:"matched"`
	Truncated bool `json:"truncated"`
	// InUse are tags the rule matched but a live or pinned release holds, so they stay.
	InUse []string `json:"in_use"`
	// Kept are tags retention protects: a recent release's image, or a protected tag pattern.
	Kept []string `json:"kept,omitempty"`
	// UnknownAge are tags on images with no creation time, which no rule ever picks.
	UnknownAge []string `json:"unknown_age"`
}

type ruleImage struct {
	digest  string
	tags    []string
	created time.Time
}

// SelectTags is a preview: it deletes nothing, and the delete that follows is previewed again.
func (s *Service) SelectTags(ctx context.Context, workspaceID uint, image string, rule SelectRule, now time.Time) (*Selection, error) {
	if rule.KeepNewest < 0 || rule.OlderThanDays < 0 {
		return nil, ErrBadRule
	}
	if rule.KeepNewest == 0 && rule.OlderThanDays == 0 {
		return nil, ErrNoRule
	}
	return s.pick(ctx, workspaceID, image, pickOptions{keepNewest: rule.KeepNewest, olderThanDays: rule.OlderThanDays}, now)
}

// pickOptions is a rule plus what retention protects beyond live and pinned releases.
type pickOptions struct {
	keepNewest    int
	olderThanDays int
	// keepDigests and keepRefs are a rollback window: images recent releases ran.
	keepDigests map[string]bool
	keepRefs    map[string]bool
	// keepPatterns protect a whole image when any of its tags matches one.
	keepPatterns []string
}

func (s *Service) pick(ctx context.Context, workspaceID uint, image string, opt pickOptions, now time.Time) (*Selection, error) {
	repo, err := repoPath(workspaceID, image)
	if err != nil {
		return nil, err
	}
	all, err := s.reg.Tags(ctx, repo)
	if err != nil {
		return nil, err
	}
	digests, failures := s.resolveDigests(ctx, repo, all)
	// A tag we can't place on an image could be the newest one, so ranking without it would be wrong.
	if len(failures) > 0 {
		tags := make([]string, 0, len(failures))
		for t := range failures {
			tags = append(tags, t)
		}
		sort.Strings(tags)
		return nil, fmt.Errorf("registry: could not read %s: %w", tags[0], failures[tags[0]])
	}
	protected, err := s.protectedIn(ctx, repo, digests)
	if err != nil {
		return nil, err
	}
	kept := refsIn(repo, digests, opt.keepDigests, opt.keepRefs)

	byDigest := map[string]*ruleImage{}
	for _, t := range all {
		d := digests[t]
		if d == "" {
			continue
		}
		if byDigest[d] == nil {
			byDigest[d] = &ruleImage{digest: d}
		}
		byDigest[d].tags = append(byDigest[d].tags, t)
		if matchesAny(opt.keepPatterns, t) {
			kept[d] = true
		}
	}
	images := make([]*ruleImage, 0, len(byDigest))
	for _, img := range byDigest {
		SortTags(img.tags)
		images = append(images, img)
	}
	s.imageTimes(ctx, workspaceID, repo, images)

	out := &Selection{Tags: []string{}, InUse: []string{}, Kept: []string{}, UnknownAge: []string{}}
	var known []*ruleImage
	for _, img := range images {
		if img.created.IsZero() {
			out.UnknownAge = append(out.UnknownAge, img.tags...)
			continue
		}
		known = append(known, img)
	}
	sort.Slice(known, func(i, j int) bool { return known[i].created.After(known[j].created) })

	cutoff := now.AddDate(0, 0, -opt.olderThanDays)
	var picked []*ruleImage
	for rank, img := range known {
		if opt.keepNewest > 0 && rank < opt.keepNewest {
			continue
		}
		if opt.olderThanDays > 0 && !img.created.Before(cutoff) {
			continue
		}
		switch {
		case protected[img.digest]:
			out.InUse = append(out.InUse, img.tags...)
		case kept[img.digest]:
			out.Kept = append(out.Kept, img.tags...)
		default:
			picked = append(picked, img)
		}
	}
	// Oldest first, so a capped selection deletes the oldest and a second run continues from there.
	sort.Slice(picked, func(i, j int) bool { return picked[i].created.Before(picked[j].created) })
	for _, img := range picked {
		out.Matched += len(img.tags)
		if len(out.Tags)+len(img.tags) > maxDeleteTags {
			out.Truncated = true
			continue
		}
		out.Tags = append(out.Tags, img.tags...)
		out.Images++
	}
	SortTags(out.InUse)
	SortTags(out.Kept)
	SortTags(out.UnknownAge)
	return out, nil
}

// refsIn maps refs recorded by tag onto this repository's digests, as protectedIn does for live releases.
func refsIn(repo string, digestOf map[string]string, digests, refs map[string]bool) map[string]bool {
	out := map[string]bool{}
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
		} else if d := digestOf[tag]; d != "" {
			out[d] = true
		}
	}
	return out
}

func matchesAny(patterns []string, tag string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, tag); err == nil && ok {
			return true
		}
	}
	return false
}

// Miabi's own build record is cheaper and truer than the config, which a reproducible build may zero.
func (s *Service) imageTimes(ctx context.Context, workspaceID uint, repo string, images []*ruleImage) {
	if s.catalog != nil {
		digests := make([]string, 0, len(images))
		for _, img := range images {
			digests = append(digests, img.digest)
		}
		if rows, err := s.catalog.ByDigests(workspaceID, digests); err == nil {
			for _, img := range images {
				if row, ok := rows[img.digest]; ok && row.BuiltAt != nil {
					img.created = *row.BuiltAt
				}
			}
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, manifestFetchConcurrency)
	for _, img := range images {
		if !img.created.IsZero() {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(img *ruleImage) {
			defer func() { <-sem; wg.Done() }()
			// A zero "created" (reproducible builds set 1970) is no age at all.
			if t, err := s.reg.ImageCreated(ctx, repo, img.digest); err == nil && t.After(time.Unix(0, 0)) {
				img.created = t
			}
		}(img)
	}
	wg.Wait()
}

// InUseError names the tags that keep a repository from being deleted.
type InUseError struct{ Tags []string }

func (e *InUseError) Error() string {
	return "registry: these tags are used by a live deployment or a pinned release: " + strings.Join(e.Tags, ", ")
}

func (e *InUseError) Is(target error) bool { return target == ErrTagInUse }

// DeleteRepository removes every image in it, or nothing at all when any image is in use.
func (s *Service) DeleteRepository(ctx context.Context, workspaceID uint, image string) (*DeleteResult, error) {
	repo, err := repoPath(workspaceID, image)
	if err != nil {
		return nil, err
	}
	all, err := s.reg.Tags(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := &DeleteResult{Tags: []TagOutcome{}, Removed: []string{}}
	chunks := chunk(all, maxDeleteTags)
	var inUse []string
	for _, c := range chunks {
		res, err := s.DeleteTags(ctx, workspaceID, image, c, true)
		if err != nil {
			return nil, err
		}
		for _, o := range res.Tags {
			if o.Result == TagInUse {
				inUse = append(inUse, o.Tag)
			}
		}
	}
	if len(inUse) > 0 {
		SortTags(inUse)
		return nil, &InUseError{Tags: inUse}
	}
	for _, c := range chunks {
		res, err := s.DeleteTags(ctx, workspaceID, image, c, false)
		if err != nil {
			return out, err
		}
		out.Tags = append(out.Tags, res.Tags...)
		out.Removed = append(out.Removed, res.Removed...)
		out.FreedBytes += res.FreedBytes
	}
	SortTags(out.Removed)
	return out, nil
}

func chunk(tags []string, size int) [][]string {
	var out [][]string
	for len(tags) > size {
		out = append(out, tags[:size])
		tags = tags[size:]
	}
	if len(tags) > 0 {
		out = append(out, tags)
	}
	return out
}
