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

package wsbackup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// A dropped download resumes with Range from the bytes already written, and the result is checked
// against the source's checksum, so a resumed file is the same file.
func TestDownloadResumesAndVerifies(t *testing.T) {
	payload := bytes.Repeat([]byte("miabi-migration-"), 4096)
	sum := sha256.Sum256(payload)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mb_key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		n := calls.Add(1)
		start := 0
		if rg := r.Header.Get("Range"); rg != "" {
			v, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			start = v
			if n == 2 && start != 20000 {
				t.Errorf("resume asked for byte %d, want 20000", start)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		}
		body := payload[start:]
		if n == 1 {
			// Cut the first attempt short: claim the full length, send part, then hang up.
			_, _ = w.Write(body[:20000])
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := newMigrationClient(srv.URL, "mb_key", false, "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(t.TempDir(), "art")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := models.MigrationArtifact{File: "data.tar.gz", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:])}
	if err := c.download(context.Background(), "shop", 1, 0, a, f); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, payload) {
		t.Fatalf("resumed file differs: %d bytes, want %d", len(got), len(payload))
	}
	if calls.Load() < 2 {
		t.Fatalf("the download was not retried (%d calls)", calls.Load())
	}

	bad := a
	bad.SHA256 = strings.Repeat("0", 64)
	g, _ := os.CreateTemp(t.TempDir(), "bad")
	defer g.Close()
	calls.Store(1)
	if err := c.download(context.Background(), "shop", 1, 0, bad, g); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a corrupted artifact must fail its checksum, got %v", err)
	}
}

// A source's error envelope reaches the operator as what the source said, and an old Miabi without
// the migration API is named as such.
func TestRemoteErrorMessages(t *testing.T) {
	err := remoteError(403, []byte(`{"success":false,"error":{"code":"MIGRATION_KEY_SCOPE","message":"this migration key does not open that workspace"}}`))
	if !strings.Contains(err.Error(), "MIGRATION_KEY_SCOPE") || !strings.Contains(err.Error(), "does not open") {
		t.Errorf("envelope error = %v", err)
	}
	if err := remoteError(401, []byte(`{"success":false,"error":{"code":"UNAUTHORIZED","message":"invalid API key"}}`)); !strings.Contains(err.Error(), "Start the migration again") {
		t.Errorf("401 = %v", err)
	}
	if err := remoteError(404, []byte("404 page not found")); !strings.Contains(err.Error(), "no migration API") {
		t.Errorf("404 = %v", err)
	}
}

func TestPickSourceWorkspace(t *testing.T) {
	one := &MigrationSourceInfo{Workspaces: []MigrationWorkspace{{Name: "shop"}}}
	if w, err := pickSourceWorkspace(one, ""); err != nil || w.Name != "shop" {
		t.Errorf("a single workspace should be picked: %v %v", w, err)
	}
	two := &MigrationSourceInfo{Workspaces: []MigrationWorkspace{{Name: "a"}, {Name: "b"}}}
	if _, err := pickSourceWorkspace(two, ""); err == nil {
		t.Error("an instance key must name the workspace")
	}
	if _, err := pickSourceWorkspace(two, "c"); err != ErrSourceWorkspace {
		t.Errorf("unknown workspace: %v", err)
	}
}

func TestNewMigrationClientRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "miabi.example.com", "ftp://x", "https://"} {
		if _, err := newMigrationClient(u, "k", false, ""); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	if _, err := newMigrationClient("https://x", "k", false, "not a pem"); err == nil {
		t.Error("a CA bundle without certificates must be refused")
	}
}

// A code carries the URL, key and workspace in one paste, and anything else is refused rather than half-read.
func TestMigrationCodeRoundTrip(t *testing.T) {
	code := EncodeMigrationCode("https://old.example.com/", "mb_secret", "shop")
	if !strings.HasPrefix(code, "miabi-migrate:") || strings.Contains(code, "mb_secret") {
		t.Fatalf("code = %q", code)
	}
	url, key, ws, err := DecodeMigrationCode("  " + code + "\n")
	if err != nil || url != "https://old.example.com" || key != "mb_secret" || ws != "shop" {
		t.Fatalf("decode = %q %q %q %v", url, key, ws, err)
	}
	for _, bad := range []string{"", "mb_secret", "miabi-migrate:", "miabi-migrate:!!!", "miabi-migrate:" + "e30"} {
		if _, _, _, err := DecodeMigrationCode(bad); err != ErrBadMigrationCode {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	src := MigrationSource{Code: code}
	if ws, err := src.resolve(); err != nil || ws != "shop" || src.URL != "https://old.example.com" || src.Key != "mb_secret" {
		t.Errorf("resolve = %q %v %+v", ws, err, src)
	}
}
