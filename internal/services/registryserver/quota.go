// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"sync"
	"time"
)

// usageTTL is how long a namespace's computed size is trusted before a refresh.
const usageTTL = 60 * time.Second

// usageCache memoizes per-workspace registry usage (bytes) with a short TTL, so
// the per-request quota check is a cheap map lookup rather than a registry walk.
type usageCache struct {
	mu      sync.Mutex
	bytes   map[uint]int64
	at      map[uint]time.Time
	refresh map[uint]bool // in-flight refresh guard
	quotaMB int           // last-seen quota (0 = unlimited)
	quotaAt time.Time
	nowFn   func() time.Time
}

func newUsageCache() *usageCache {
	return &usageCache{
		bytes: map[uint]int64{}, at: map[uint]time.Time{}, refresh: map[uint]bool{},
		nowFn: time.Now,
	}
}

// NamespaceUsageBytes computes a workspace's registry usage: the sum of the
// unique blob sizes across all its repositories' tags (deduped by digest). It is
// an estimate (manifest-reported sizes), suitable for soft quota enforcement.
func (s *Service) NamespaceUsageBytes(ctx context.Context, workspaceID uint) (int64, error) {
	repos, err := s.ListRepositories(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	prefix := Namespace(workspaceID) + "/"
	seen := make(map[string]int64)
	for _, r := range repos {
		for _, tag := range r.Tags {
			sizes, err := s.reg.BlobSizes(ctx, prefix+r.Name, tag)
			if err != nil {
				continue
			}
			for d, sz := range sizes {
				seen[d] = sz
			}
		}
	}
	var total int64
	for _, sz := range seen {
		total += sz
	}
	return total, nil
}

// quotaExceeded reports whether the workspace is over its registry quota. It is soft and non-blocking: it
// reads a TTL cache and, on a miss/stale entry, triggers an async refresh and returns false (fail-open) so
// a push is never blocked on a registry walk. nil-safe for tests that don't wire the cache.
func (s *Service) quotaExceeded(workspaceID uint) bool {
	if s.usage == nil || s.reg == nil || s.repo == nil {
		return false
	}
	quotaMB := s.quotaMB()
	if quotaMB <= 0 {
		return false // unlimited
	}
	limit := int64(quotaMB) * 1024 * 1024

	s.usage.mu.Lock()
	bytes, ok := s.usage.bytes[workspaceID]
	fresh := ok && s.usage.nowFn().Sub(s.usage.at[workspaceID]) < usageTTL
	if !fresh && !s.usage.refresh[workspaceID] {
		s.usage.refresh[workspaceID] = true
		go s.refreshUsage(workspaceID)
	}
	s.usage.mu.Unlock()

	return fresh && bytes >= limit
}

// quotaMB returns the configured per-workspace quota, cached briefly so the hot
// path avoids a DB read per request.
func (s *Service) quotaMB() int {
	s.usage.mu.Lock()
	if s.usage.nowFn().Sub(s.usage.quotaAt) < usageTTL {
		q := s.usage.quotaMB
		s.usage.mu.Unlock()
		return q
	}
	s.usage.mu.Unlock()

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

func (s *Service) refreshUsage(workspaceID uint) {
	defer func() {
		s.usage.mu.Lock()
		delete(s.usage.refresh, workspaceID)
		s.usage.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	total, err := s.NamespaceUsageBytes(ctx, workspaceID)
	if err != nil {
		return
	}
	s.usage.mu.Lock()
	s.usage.bytes[workspaceID] = total
	s.usage.at[workspaceID] = s.usage.nowFn()
	s.usage.mu.Unlock()
}
