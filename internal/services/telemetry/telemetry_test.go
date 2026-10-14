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
package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newServer(t *testing.T) (*httptest.Server, *atomic.Int32, *Payload) {
	t.Helper()
	var hits atomic.Int32
	var got Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &got
}

func TestInstallationIDIsStableAndNotRaw(t *testing.T) {
	id := InstallationID("mbi_abc")
	if id != InstallationID("mbi_abc") {
		t.Fatal("installation id is not stable")
	}
	if len(id) != 64 || strings.Contains(id, "mbi_abc") {
		t.Fatalf("unexpected installation id %q", id)
	}
	if id == InstallationID("mbi_abd") {
		t.Fatal("different install ids collide")
	}
}

func TestSendPostsMinimalPayload(t *testing.T) {
	srv, hits, got := newServer(t)
	s := New(Options{
		URL: srv.URL, Version: "1.8.0", InstallID: "mbi_abc", Enabled: true,
		Edition:  func() string { return "enterprise" },
		Platform: func() string { return PlatformSwarm },
	})
	if err := s.Send(context.Background(), EventStartup); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	want := Payload{
		Schema: SchemaVersion, InstallationID: InstallationID("mbi_abc"), Event: EventStartup,
		Version: "1.8.0", Edition: "enterprise", Platform: PlatformSwarm,
		OS: got.OS, Arch: got.Arch,
	}
	if *got != want {
		t.Fatalf("payload = %+v, want %+v", *got, want)
	}
}

func TestSendSkipped(t *testing.T) {
	cases := map[string]Options{
		"disabled":   {Version: "1.8.0", InstallID: "mbi_abc", Enabled: false},
		"dev build":  {Version: "dev", InstallID: "mbi_abc", Enabled: true},
		"sha build":  {Version: "3f2a9c1", InstallID: "mbi_abc", Enabled: true},
		"no install": {Version: "1.8.0", Enabled: true},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			srv, hits, _ := newServer(t)
			opts.URL = srv.URL
			if err := New(opts).Send(context.Background(), EventDaily); err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 0 {
				t.Fatalf("hits = %d, want 0", hits.Load())
			}
		})
	}
}

func TestSendReportsErrorsWithoutPanicking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := New(Options{URL: srv.URL, Version: "1.8.0", InstallID: "mbi_abc", Enabled: true})
	if err := s.Send(context.Background(), EventDaily); err == nil {
		t.Fatal("expected an error for a 500")
	}

	s = New(Options{URL: "http://127.0.0.1:1", Version: "1.8.0", InstallID: "mbi_abc", Enabled: true})
	if err := s.Send(context.Background(), EventDaily); err == nil {
		t.Fatal("expected an error for an unreachable endpoint")
	}
}

func TestPayloadDefaults(t *testing.T) {
	p := New(Options{Version: "v1.8.0", InstallID: "mbi_abc"}).Payload(EventDaily)
	if p.Version != "1.8.0" || p.Edition != "community" || p.Platform != PlatformStandalone {
		t.Fatalf("unexpected defaults %+v", p)
	}
}
