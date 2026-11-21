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
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// usageTTL is how long a namespace's index is trusted before a background refresh.
	usageTTL = 5 * time.Minute
	// A push marks the index dirty; one walk per this long keeps a push of many blobs from triggering many walks.
	usageDebounce = 30 * time.Second
	// A walk past this keeps the last complete index instead.
	usageWalkTimeout = 60 * time.Second
)

// usageIndex is one walk of a namespace: what every tag points at, and what each image is made of.
type usageIndex struct {
	tags  map[string]map[string]string // repository -> tag -> manifest digest
	blobs map[string]map[string]int64  // manifest digest -> blob digest -> size
	at    time.Time
}

type usageCache struct {
	mu      sync.Mutex
	index   map[uint]*usageIndex
	dirty   map[uint]time.Time // last push or delete
	refresh map[uint]bool      // in-flight refresh guard
	quotaMB int                // last-seen quota (0 = unlimited)
	quotaAt time.Time
	nowFn   func() time.Time
}

func newUsageCache() *usageCache {
	return &usageCache{
		index: map[uint]*usageIndex{}, dirty: map[uint]time.Time{}, refresh: map[uint]bool{},
		nowFn: time.Now,
	}
}

// Usage is a workspace's registry storage, deduplicated by blob.
type Usage struct {
	TotalBytes   int64       `json:"total_bytes"`
	QuotaBytes   int64       `json:"quota_bytes"` // 0 = unlimited
	Repositories []RepoUsage `json:"repositories"`
	ComputedAt   *time.Time  `json:"computed_at,omitempty"`
	// Stale means a refresh is running and the numbers are from ComputedAt.
	Stale bool `json:"stale"`
}

type RepoUsage struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	// ExclusiveBytes is referenced by no other repository in the workspace: what deleting this one frees.
	ExclusiveBytes int64 `json:"exclusive_bytes"`
	Tags           int   `json:"tags"`
}

// Usage returns the cached index, building it on first use; later reads never wait on a walk.
func (s *Service) Usage(ctx context.Context, workspaceID uint) (*Usage, error) {
	var idx *usageIndex
	stale := false
	if s.peekIndex(workspaceID) == nil {
		var err error
		if idx, err = s.rebuildIndex(ctx, workspaceID); err != nil {
			return nil, err
		}
	} else {
		idx, stale = s.cachedIndex(workspaceID)
	}
	out := idx.usage()
	out.Stale = stale
	if mb := s.quotaMB(); mb > 0 {
		out.QuotaBytes = int64(mb) * 1024 * 1024
	}
	return out, nil
}

// CachedUsage is what is known without walking the registry, for a list of many workspaces.
func (s *Service) CachedUsage(workspaceID uint) *Usage {
	idx, stale := s.cachedIndex(workspaceID)
	if idx == nil {
		return nil
	}
	out := idx.usage()
	out.Stale = stale
	return out
}

// WarmUsage starts computing a workspace's usage in the background if nothing is cached yet.
func (s *Service) WarmUsage(workspaceID uint) {
	if s.usage == nil {
		return
	}
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	if s.usage.index[workspaceID] == nil {
		s.startRefreshLocked(workspaceID)
	}
}

// NamespaceUsageBytes is the deduplicated size of every image in the workspace's namespace.
func (s *Service) NamespaceUsageBytes(ctx context.Context, workspaceID uint) (int64, error) {
	idx, err := s.buildIndex(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	return idx.total(), nil
}

// Fail-open: a stale entry triggers an async refresh and returns false, so pushes never wait on a registry walk.
func (s *Service) quotaExceeded(workspaceID uint) bool {
	if s.usage == nil || s.reg == nil {
		return false
	}
	quotaMB := s.quotaMB()
	if quotaMB <= 0 {
		return false // unlimited
	}
	idx, stale := s.cachedIndex(workspaceID)
	return idx != nil && !stale && idx.total() >= int64(quotaMB)*1024*1024
}

// cachedIndex reports the index and whether it is stale, starting a background refresh when it is.
func (s *Service) cachedIndex(workspaceID uint) (*usageIndex, bool) {
	if s.usage == nil {
		return nil, false
	}
	c := s.usage
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.index[workspaceID]
	now := c.nowFn()
	stale := idx == nil || now.Sub(idx.at) >= usageTTL ||
		(c.dirty[workspaceID].After(idx.at) && now.Sub(idx.at) >= usageDebounce)
	if stale {
		s.startRefreshLocked(workspaceID)
	}
	return idx, stale
}

func (s *Service) peekIndex(workspaceID uint) *usageIndex {
	if s.usage == nil {
		return nil
	}
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	return s.usage.index[workspaceID]
}

// Caller holds s.usage.mu.
func (s *Service) startRefreshLocked(workspaceID uint) {
	if s.reg == nil || s.usage.refresh[workspaceID] {
		return
	}
	s.usage.refresh[workspaceID] = true
	go s.refreshIndex(workspaceID)
}

// forgetUsage refreshes at once, so usage drops right after a delete rather than after the TTL.
func (s *Service) forgetUsage(workspaceID uint) {
	if s.usage == nil {
		return
	}
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	s.usage.dirty[workspaceID] = s.usage.nowFn()
	s.startRefreshLocked(workspaceID)
}

func (s *Service) refreshIndex(workspaceID uint) {
	defer func() {
		s.usage.mu.Lock()
		delete(s.usage.refresh, workspaceID)
		s.usage.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), usageWalkTimeout)
	defer cancel()
	_, _ = s.rebuildIndex(ctx, workspaceID)
}

func (s *Service) rebuildIndex(ctx context.Context, workspaceID uint) (*usageIndex, error) {
	idx, err := s.buildIndex(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if s.usage != nil {
		s.usage.mu.Lock()
		s.usage.index[workspaceID] = idx
		s.usage.mu.Unlock()
	}
	return idx, nil
}

// markDirty makes the next read refresh the index, once the debounce has passed.
func (s *Service) markDirty(workspaceID uint) {
	if s.usage == nil {
		return
	}
	s.usage.mu.Lock()
	s.usage.dirty[workspaceID] = s.usage.nowFn()
	s.usage.mu.Unlock()
}

// One manifest read per tag; tags on an image already read cost nothing more.
func (s *Service) buildIndex(ctx context.Context, workspaceID uint) (*usageIndex, error) {
	repos, err := s.ListRepositories(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	prefix := Namespace(workspaceID) + "/"
	idx := &usageIndex{tags: map[string]map[string]string{}, blobs: map[string]map[string]int64{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, manifestFetchConcurrency)
	for _, r := range repos {
		idx.tags[r.Name] = map[string]string{}
		for _, tag := range r.Tags {
			wg.Add(1)
			sem <- struct{}{}
			go func(repo, tag string) {
				defer func() { <-sem; wg.Done() }()
				digest, sizes, err := s.reg.manifestBlobs(ctx, prefix+repo, tag)
				if err != nil || digest == "" {
					return
				}
				mu.Lock()
				idx.tags[repo][tag] = digest
				idx.blobs[digest] = sizes
				mu.Unlock()
			}(r.Name, tag)
		}
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	idx.at = time.Now()
	if s.usage != nil {
		idx.at = s.usage.nowFn()
	}
	return idx, nil
}

func (idx *usageIndex) total() int64 {
	return sumBlobs(idx.blobsOf(func(string) bool { return true }))
}

// blobsOf unions the blobs of every image in the repositories keep selects.
func (idx *usageIndex) blobsOf(keep func(repo string) bool) map[string]int64 {
	out := map[string]int64{}
	for repo, tags := range idx.tags {
		if !keep(repo) {
			continue
		}
		for _, d := range tags {
			for b, sz := range idx.blobs[d] {
				out[b] = sz
			}
		}
	}
	return out
}

func (idx *usageIndex) usage() *Usage {
	at := idx.at
	out := &Usage{TotalBytes: idx.total(), Repositories: []RepoUsage{}, ComputedAt: &at}
	for repo, tags := range idx.tags {
		mine := idx.blobsOf(func(r string) bool { return r == repo })
		others := idx.blobsOf(func(r string) bool { return r != repo })
		exclusive := int64(0)
		for b, sz := range mine {
			if _, shared := others[b]; !shared {
				exclusive += sz
			}
		}
		out.Repositories = append(out.Repositories, RepoUsage{Name: repo, Bytes: sumBlobs(mine), ExclusiveBytes: exclusive, Tags: len(tags)})
	}
	sort.Slice(out.Repositories, func(i, j int) bool {
		if out.Repositories[i].Bytes != out.Repositories[j].Bytes {
			return out.Repositories[i].Bytes > out.Repositories[j].Bytes
		}
		return out.Repositories[i].Name < out.Repositories[j].Name
	})
	return out
}

// freedBy is what deleting digests from repo releases: blobs no image left anywhere in the namespace still uses.
func (idx *usageIndex) freedBy(repo string, digests map[string]bool) int64 {
	gone := map[string]int64{}
	for d := range digests {
		for b, sz := range idx.blobs[d] {
			gone[b] = sz
		}
	}
	for r, tags := range idx.tags {
		for _, d := range tags {
			if r == repo && digests[d] {
				continue
			}
			for b := range idx.blobs[d] {
				delete(gone, b)
			}
		}
	}
	return sumBlobs(gone)
}

func sumBlobs(m map[string]int64) int64 {
	var total int64
	for _, sz := range m {
		total += sz
	}
	return total
}

// Cached briefly so the hot path avoids a DB read per request.
func (s *Service) quotaMB() int {
	if s.usage == nil {
		return 0
	}
	s.usage.mu.Lock()
	if s.usage.nowFn().Sub(s.usage.quotaAt) < time.Minute {
		q := s.usage.quotaMB
		s.usage.mu.Unlock()
		return q
	}
	s.usage.mu.Unlock()

	if s.repo == nil {
		return 0
	}
	st, err := s.Get()
	if err != nil {
		return 0
	}
	s.usage.mu.Lock()
	s.usage.quotaMB = st.PerWorkspaceQuotaMB
	s.usage.quotaAt = s.usage.nowFn()
	s.usage.mu.Unlock()
	return st.PerWorkspaceQuotaMB
}

// Namespaces lists the workspaces that have a namespace in the registry, for the admin view.
func (s *Service) Namespaces(ctx context.Context) ([]uint, error) {
	all, err := s.reg.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[uint]bool{}
	for _, repo := range all {
		ns, _, ok := strings.Cut(repo, "/")
		if !ok {
			continue
		}
		if id, ok := parseIDNamespace(ns); ok {
			seen[id] = true
		}
	}
	out := make([]uint, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
