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

package remote

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

const defaultTTL = 24 * time.Hour

type entitlementChecker interface {
	Has(flag string) bool
}

// Store keeps the synced marketplace catalog
type Store struct {
	configured string
	official   string
	ee         entitlementChecker

	cache Cache
	ttl   time.Duration

	mu        sync.RWMutex
	client    *Client
	activeURL string
	denied    bool
	etag      string
	syncedAt  time.Time // last successful live sync, 304 included; zero before one
	generated string    // the bundle's generatedAt
	templates []DecodedTemplate
	index     map[string]*DecodedTemplate // name -> template
}

func New(baseURL string, cache Cache) *Store {
	s := &Store{configured: baseURL, cache: cache, ttl: defaultTTL, index: map[string]*DecodedTemplate{}}
	s.resolve()
	return s
}

func (s *Store) SetEntitlements(ee entitlementChecker, officialURL string) {
	s.mu.Lock()
	s.ee, s.official = ee, strings.TrimSpace(officialURL)
	s.mu.Unlock()
	s.resolve()
}

func (s *Store) effectiveURL() (url string, denied bool) {
	s.mu.RLock()
	configured, official, ee := strings.TrimSpace(s.configured), s.official, s.ee
	s.mu.RUnlock()

	if configured == "" || official == "" || sameMarketplace(configured, official) {
		return configured, false
	}
	if ee != nil && ee.Has(enterprise.FlagPrivateRegistry) {
		return configured, false
	}
	return official, true
}

// Normalizes so a trailing slash or case difference is not read as a custom catalog.
func sameMarketplace(a, b string) bool {
	return strings.EqualFold(resolveExportURL(a), resolveExportURL(b))
}

func (s *Store) resolve() {
	url, denied := s.effectiveURL()

	s.mu.Lock()
	defer s.mu.Unlock()
	wasDenied := s.denied
	s.denied = denied
	if url == s.activeURL && (s.client != nil) == (url != "") {
		if denied && !wasDenied {
			logCustomMarketplaceDenied(s.configured, url)
		}
		return
	}
	s.activeURL = url
	s.client = nil
	if url != "" {
		s.client = NewClient(url)
	}
	s.etag = ""
	s.templates = nil
	s.index = map[string]*DecodedTemplate{}
	if denied {
		logCustomMarketplaceDenied(s.configured, url)
	}
}

func logCustomMarketplaceDenied(configured, fallback string) {
	logger.Warn("marketplace: a custom marketplace requires an Enterprise license (the private_registry entitlement); "+
		"syncing from the official catalog instead",
		"configured", configured, "using", fallback)
}

// Enabled reports whether a marketplace URL is configured (sync active).
func (s *Store) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client != nil
}

func (s *Store) Source() (url string, denied bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeURL, s.denied
}

// Lets a restart serve community templates before the first live sync completes.
func (s *Store) LoadCache(ctx context.Context) error {
	if s.cache == nil {
		return nil
	}
	s.resolve()
	s.mu.RLock()
	source := s.activeURL
	s.mu.RUnlock()
	if source == "" {
		return nil
	}
	// A bundle cached from a marketplace this install may no longer sync from must not be served.
	data, etag, err := s.cache.Load(ctx, source)
	if err != nil || len(data) == 0 {
		return err
	}
	return s.set(data, etag)
}

func (s *Store) Sync(ctx context.Context) error {
	// A license installed or lapsed since the last sync changes the allowed catalog without a restart.
	s.resolve()

	s.mu.RLock()
	client, source, etag := s.client, s.activeURL, s.etag
	s.mu.RUnlock()
	if client == nil {
		return nil
	}

	data, newETag, notModified, err := client.Fetch(ctx, etag)
	if err != nil {
		return err
	}
	if notModified {
		s.markSynced()
		return nil
	}
	if err := s.set(data, newETag); err != nil {
		return err
	}
	s.markSynced()
	if s.cache != nil {
		if err := s.cache.Save(ctx, source, data, newETag, s.ttl); err != nil {
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
	var meta struct {
		GeneratedAt string `json:"generatedAt"`
	}
	_ = json.Unmarshal(data, &meta)
	s.mu.Lock()
	s.templates, s.index, s.etag, s.generated = tpls, idx, etag, meta.GeneratedAt
	s.mu.Unlock()
	return nil
}

func (s *Store) markSynced() {
	s.mu.Lock()
	s.syncedAt = time.Now()
	s.mu.Unlock()
}

func (s *Store) CatalogInfo() (templates int, syncedAt time.Time, generatedAt string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.templates), s.syncedAt, s.generated
}

// Templates returns the decoded synced templates (official + community).
func (s *Store) Templates() []DecodedTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.templates
}

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
