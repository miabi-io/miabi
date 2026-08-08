// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

// defaultTTL bounds how long a cached bundle is trusted without a refresh. It is
// only a safety net: a healthy install refreshes well within it via the sync
// cron, and a 304 keeps serving the decoded view regardless.
const defaultTTL = 24 * time.Hour

// Store keeps the synced marketplace catalog: it fetches the export bundle conditionally, caches it in
// Redis, and serves a decoded, digest-verified in-memory view. A nil client (empty MIABI_MARKETPLACE_URL)
// is the air-gapped kill switch: Sync is a no-op and the catalog falls back to the embedded floor.
type Store struct {
	client *Client
	cache  Cache
	ttl    time.Duration

	mu        sync.RWMutex
	etag      string
	templates []DecodedTemplate
	index     map[string]*DecodedTemplate // name -> template
}

// New builds a store for the marketplace at baseURL, backed by cache. An empty
// baseURL disables syncing (embedded-only). cache may be nil to disable
// persistence (decoded view is then in-memory only).
func New(baseURL string, cache Cache) *Store {
	s := &Store{cache: cache, ttl: defaultTTL, index: map[string]*DecodedTemplate{}}
	if strings.TrimSpace(baseURL) != "" {
		s.client = NewClient(baseURL)
	}
	return s
}

// Enabled reports whether a marketplace URL is configured (sync active).
func (s *Store) Enabled() bool { return s.client != nil }

// LoadCache populates the in-memory view from a previously-synced bundle, so a
// restart can serve community templates before the first live sync completes.
func (s *Store) LoadCache(ctx context.Context) error {
	if s.cache == nil {
		return nil
	}
	data, etag, err := s.cache.Load(ctx)
	if err != nil || len(data) == 0 {
		return err
	}
	return s.set(data, etag)
}

// Sync fetches the export bundle conditionally and refreshes the cache and the
// in-memory view. A 304 (ETag match) is a no-op. Disabled stores return nil.
func (s *Store) Sync(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	s.mu.RLock()
	etag := s.etag
	s.mu.RUnlock()

	data, newETag, notModified, err := s.client.Fetch(ctx, etag)
	if err != nil {
		return err
	}
	if notModified {
		return nil
	}
	if err := s.set(data, newETag); err != nil {
		return err
	}
	if s.cache != nil {
		if err := s.cache.Save(ctx, data, newETag, s.ttl); err != nil {
			logger.Warn("marketplace: failed to cache bundle", "error", err)
		}
	}
	logger.Info("marketplace: synced registry bundle", "templates", len(s.Templates()), "etag", newETag)
	return nil
}

func (s *Store) set(data []byte, etag string) error {
	tpls, err := decode(data)
	if err != nil {
		return err
	}
	idx := make(map[string]*DecodedTemplate, len(tpls))
	for i := range tpls {
		idx[tpls[i].Name] = &tpls[i]
	}
	s.mu.Lock()
	s.templates, s.index, s.etag = tpls, idx, etag
	s.mu.Unlock()
	return nil
}

// Templates returns the decoded synced templates (official + community).
func (s *Store) Templates() []DecodedTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.templates
}

// Manifest resolves a synced manifest (empty version = latest), reporting the
// template's source label. A non-empty version must match exactly.
func (s *Store) Manifest(name, version string) (*manifest.Manifest, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.index[name]
	if t == nil || len(t.Versions) == 0 {
		return nil, "", false
	}
	if version == "" {
		return t.Versions[0].Manifest, t.Source, true
	}
	for _, v := range t.Versions {
		if v.Version == version {
			return v.Manifest, t.Source, true
		}
	}
	return nil, "", false
}
