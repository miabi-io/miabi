// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TagManifest must fetch the digest's manifest and re-PUT the identical bytes +
// media type under the new tag — adding a tag without re-uploading blobs.
func TestTagManifest(t *testing.T) {
	const (
		repo   = "ws_7/nginx"
		digest = "sha256:abc"
		tag    = "v6"
		body   = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
		ctype  = "application/vnd.oci.image.manifest.v1+json"
	)
	var putBody, putCT string
	putCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/"+repo+"/manifests/"+digest:
			w.Header().Set("Content-Type", ctype)
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodPut && r.URL.Path == "/v2/"+repo+"/manifests/"+tag:
			putCalled = true
			putCT = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			putBody = string(b)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, nil).TagManifest(context.Background(), repo, digest, tag); err != nil {
		t.Fatalf("TagManifest: %v", err)
	}
	if !putCalled {
		t.Fatal("expected a PUT of the tag manifest")
	}
	if putBody != body {
		t.Errorf("PUT body = %q, want the digest's manifest bytes %q", putBody, body)
	}
	if putCT != ctype {
		t.Errorf("PUT content-type = %q, want %q", putCT, ctype)
	}
}
