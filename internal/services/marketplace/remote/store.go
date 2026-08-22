// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
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

// sameMarketplace compares two marketplace URLs the way the fetcher does, so a
// trailing slash or a case difference in the scheme/host is not read as a custom
// catalog.
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

// Source returns the marketplace actually being synced from, and whether a
// configured custom one was refused for want of a license.
func (s *Store) Source() (url string, denied bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeURL, s.denied
}

// LoadCache populates the in-memory view from a previously-synced bundle, so a
// restart can serve community templates before the first live sync completes.
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
	// Scoped to the source: a bundle cached from a marketplace this install may no
	// longer sync from must not be served after the fallback.
	data, etag, err := s.cache.Load(ctx, source)
	if err != nil || len(data) == 0 {
		return err
	}
	return s.set(data, etag)
}

// Sync fetches the export bundle conditionally and refreshes the cache and the
// in-memory view. A 304 (ETag match) is a no-op. Disabled stores return nil.
func (s *Store) Sync(ctx context.Context) error {
	// Re-resolve first: a license installed or lapsed since the last sync changes
	// which catalog this install may pull from, without a restart.
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
		return nil
	}
	if err := s.set(data, newETag); err != nil {
		return err
	}
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
