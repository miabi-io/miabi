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
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

const sampleManifest = `apiVersion: miabi.io/v1
kind: Template
metadata:
  name: cooltool
  displayName: Cool Tool
  version: 1.2.0
  category: Web
applications:
  - name: app
    image: cooltool
    tag: latest
    ports:
      - container: 8080
        scheme: http
`

// bundleJSON digests the manifest like the real service; badDigest forces a digest mismatch.
func bundleJSON(t *testing.T, etag string, badDigest bool) []byte {
	t.Helper()
	digest := manifest.Digest([]byte(sampleManifest))
	if badDigest {
		digest = "sha256:deadbeef"
	}
	b := Bundle{
		ETag:        etag,
		GeneratedAt: "2026-06-22T00:00:00Z",
		Templates: []BundleTemplate{{
			Name:   "cooltool",
			Source: SourceOfficial,
			Versions: []BundleVersion{{
				Version:  "1.2.0",
				Digest:   digest,
				Manifest: sampleManifest,
			}},
		}},
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	return data
}

func exportServer(t *testing.T, etag string, badDigest bool) (*httptest.Server, *int) {
	t.Helper()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != exportPath {
			http.NotFound(w, r)
			return
		}
		hits++
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bundleJSON(t, etag, badDigest))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestStoreSyncDecodesAndResolves(t *testing.T) {
	srv, _ := exportServer(t, `"v1"`, false)
	s := New(srv.URL, nil)
	if !s.Enabled() {
		t.Fatal("store should be enabled with a URL")
	}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	tpls := s.Templates()
	if len(tpls) != 1 || tpls[0].Name != "cooltool" || tpls[0].Source != SourceOfficial {
		t.Fatalf("unexpected templates: %+v", tpls)
	}
	m, src, ok := s.Manifest("cooltool", "")
	if !ok || src != SourceOfficial || m.Metadata.Version != "1.2.0" {
		t.Fatalf("resolve latest: m=%v src=%q ok=%v", m, src, ok)
	}
	if _, _, ok := s.Manifest("cooltool", "9.9.9"); ok {
		t.Fatal("unknown version should not resolve")
	}
}

func TestStoreSyncConditional304(t *testing.T) {
	srv, hits := exportServer(t, `"v1"`, false)
	s := New(srv.URL, nil)
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if *hits != 2 {
		t.Fatalf("expected 2 requests, got %d", *hits)
	}
	// The 304 must not wipe the decoded view.
	if len(s.Templates()) != 1 {
		t.Fatalf("templates lost after 304: %+v", s.Templates())
	}
}

func TestStoreDropsDigestMismatch(t *testing.T) {
	srv, _ := exportServer(t, `"v1"`, true)
	s := New(srv.URL, nil)
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(s.Templates()) != 0 {
		t.Fatalf("tampered version should be dropped, got %+v", s.Templates())
	}
}

func TestStoreDisabled(t *testing.T) {
	s := New("", nil)
	if s.Enabled() {
		t.Fatal("empty URL should disable the store")
	}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("disabled sync should be a no-op: %v", err)
	}
	if len(s.Templates()) != 0 {
		t.Fatal("disabled store should expose no templates")
	}
}

// Cached per source, like Redis, so a test can prove one marketplace's bundle is never served for another.
type memCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	data []byte
	etag string
}

func (c *memCache) Load(_ context.Context, source string) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[source]
	return e.data, e.etag, nil
}

func (c *memCache) Save(_ context.Context, source string, data []byte, etag string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cacheEntry{}
	}
	c.entries[source] = cacheEntry{data: data, etag: etag}
	return nil
}

// get is a test helper for asserting on what was cached for a source.
func (c *memCache) get(source string) cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[source]
}

func TestStoreCacheRoundTrip(t *testing.T) {
	srv, _ := exportServer(t, `"v1"`, false)
	cache := &memCache{}

	// First store syncs and writes the cache.
	s1 := New(srv.URL, cache)
	if err := s1.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if e := cache.get(srv.URL); len(e.data) == 0 || e.etag != `"v1"` {
		t.Fatalf("cache not populated: etag=%q len=%d", e.etag, len(e.data))
	}

	// A fresh store (e.g. after restart) serves the cached bundle without syncing.
	s2 := New(srv.URL, cache)
	if err := s2.LoadCache(context.Background()); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	if _, _, ok := s2.Manifest("cooltool", "1.2.0"); !ok {
		t.Fatal("cached bundle should resolve after LoadCache")
	}
}
