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
	"sync"
	"time"
)

// usageTTL is how long a namespace's computed size is trusted before a refresh.
const usageTTL = 60 * time.Second

// A short TTL keeps the per-request quota check a map lookup rather than a registry walk.
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

// NamespaceUsageBytes is an estimate from manifest sizes, deduped by digest; fine for soft quotas.
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

// Fail-open: a stale entry triggers an async refresh and returns false, so pushes never wait on a registry walk.
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

// Cached briefly so the hot path avoids a DB read per request.
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
