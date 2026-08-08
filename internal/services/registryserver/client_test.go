// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNextLink(t *testing.T) {
	cases := map[string]string{
		`</v2/_catalog?n=200&last=foo>; rel="next"`: "/v2/_catalog?n=200&last=foo",
		`</v2/_catalog?last=x>; rel="prev"`:         "",
		``:                                          "",
	}
	for in, want := range cases {
		if got := nextLink(in); got != want {
			t.Errorf("nextLink(%q) = %q, want %q", in, got, want)
		}
	}
}

func fakeRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/_catalog", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"repositories":["ws_7/web","ws_7/api","ws_8/secret"]}`))
	})
	mux.HandleFunc("/v2/ws_7/web/tags/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"ws_7/web","tags":["latest","1.0"]}`))
	})
	mux.HandleFunc("/v2/ws_7/api/tags/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"ws_7/api","tags":["v2"]}`))
	})
	// Manifests carry the digest header (for delete) and a body with sizes (for
	// usage). Layer "A" is shared across web tags to exercise dedup.
	mux.HandleFunc("/v2/ws_7/web/manifests/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:wlatest")
		_, _ = w.Write([]byte(`{"config":{"digest":"sha256:cfgW","size":100},"layers":[{"digest":"sha256:A","size":1000},{"digest":"sha256:B","size":2000}]}`))
	})
	mux.HandleFunc("/v2/ws_7/web/manifests/1.0", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Docker-Content-Digest", "sha256:abc")
			_, _ = w.Write([]byte(`{"config":{"digest":"sha256:cfgW2","size":100},"layers":[{"digest":"sha256:A","size":1000},{"digest":"sha256:C","size":3000}]}`))
		default:
			http.Error(w, "no", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v2/ws_7/api/manifests/v2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:av2")
		_, _ = w.Write([]byte(`{"config":{"digest":"sha256:cfgA","size":50},"layers":[{"digest":"sha256:D","size":500}]}`))
	})
	mux.HandleFunc("/v2/ws_7/web/manifests/sha256:abc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		http.Error(w, "no", http.StatusMethodNotAllowed)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func listSvc(t *testing.T) *Service {
	return &Service{reg: NewClient(fakeRegistry(t).URL)}
}

func TestListRepositoriesFiltersNamespace(t *testing.T) {
	repos, err := listSvc(t).ListRepositories(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	// Only ws_7 repos, namespace stripped, sorted; ws_8/secret excluded.
	if len(repos) != 2 || repos[0].Name != "api" || repos[1].Name != "web" {
		t.Fatalf("repos = %+v, want [api web]", repos)
	}
	if len(repos[1].Tags) != 2 {
		t.Errorf("web tags = %v", repos[1].Tags)
	}
	// Display order, not lexicographic: "latest" leads (see SortTags).
	if repos[1].Tags[0] != "latest" {
		t.Errorf("tags not in display order: %v, want latest first", repos[1].Tags)
	}
}

func TestDeleteTagResolvesDigest(t *testing.T) {
	if err := listSvc(t).DeleteTag(context.Background(), 7, "web", "1.0"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
}

func TestNamespaceUsageBytesDedupes(t *testing.T) {
	got, err := listSvc(t).NamespaceUsageBytes(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	// Unique blobs across ws_7: cfgA50 + D500 + cfgW100 + A1000 + B2000 +
	// cfgW2100 + C3000 (layer A shared between web tags, counted once).
	const want = 50 + 500 + 100 + 1000 + 2000 + 100 + 3000
	if got != want {
		t.Errorf("usage = %d, want %d", got, want)
	}
}
