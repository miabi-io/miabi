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
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveExportURL(t *testing.T) {
	cases := map[string]string{
		"https://marketplace.miabi.io":        "https://marketplace.miabi.io/v1/export",
		"https://marketplace.miabi.io/":       "https://marketplace.miabi.io/v1/export",
		"  https://srv/ ":                     "https://srv/v1/export",
		"https://cdn.jsdelivr.net/x.json":     "https://cdn.jsdelivr.net/x.json",
		"https://cdn.jsdelivr.net/X.JSON":     "https://cdn.jsdelivr.net/X.JSON",
		"https://cdn/gh/o/r@main/export.json": "https://cdn/gh/o/r@main/export.json",
	}
	for in, want := range cases {
		if got := resolveExportURL(in); got != want {
			t.Errorf("resolveExportURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFetchStaticJSON(t *testing.T) {
	const etag = `"static-v1"`
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte(`{"etag":"x","templates":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL + "/export.json")
	_, got, notModified, err := c.Fetch(context.Background(), "")
	if err != nil || notModified {
		t.Fatalf("first fetch: notModified=%v err=%v", notModified, err)
	}
	if hitPath != "/export.json" {
		t.Fatalf("expected direct /export.json, hit %q", hitPath)
	}
	if got != etag {
		t.Fatalf("etag = %q, want %q", got, etag)
	}
	if _, _, nm, err := c.Fetch(context.Background(), etag); err != nil || !nm {
		t.Fatalf("conditional fetch should be 304: notModified=%v err=%v", nm, err)
	}
}
