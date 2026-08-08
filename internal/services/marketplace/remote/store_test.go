// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// bundleJSON builds an export bundle with one official template, digesting the
// manifest the way the real service would. badDigest forces a digest mismatch.
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

type memCache struct {
	mu   sync.Mutex
	data []byte
	etag string
}

func (c *memCache) Load(context.Context) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data, c.etag, nil
}

func (c *memCache) Save(_ context.Context, data []byte, etag string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data, c.etag = data, etag
	return nil
}

func TestStoreCacheRoundTrip(t *testing.T) {
	srv, _ := exportServer(t, `"v1"`, false)
	cache := &memCache{}

	// First store syncs and writes the cache.
	s1 := New(srv.URL, cache)
	if err := s1.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(cache.data) == 0 || cache.etag != `"v1"` {
		t.Fatalf("cache not populated: etag=%q len=%d", cache.etag, len(cache.data))
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
