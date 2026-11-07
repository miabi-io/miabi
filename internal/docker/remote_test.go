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

package docker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRemoteOverTransport(t *testing.T) {
	mux := http.NewServeMux()
	ping := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", "1.43")
		w.Header().Set("Ostype", "linux")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}
	mux.HandleFunc("/_ping", ping)
	// Match any version-prefixed path; serve /info and /_ping.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			ping(w, r)
		case strings.HasSuffix(r.URL.Path, "/info"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ServerVersion":"27.5.1","OperatingSystem":"Test OS","Architecture":"x86_64","NCPU":4,"MemTotal":2048,"Containers":3,"ContainersRunning":2,"Images":5}`))
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	cl, err := NewRemote(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	defer cl.Close()

	info, err := cl.Info(context.Background())
	if err != nil {
		t.Fatalf("Info over remote transport: %v", err)
	}
	if info.Version != "27.5.1" || info.CPUs != 4 || info.Containers != 3 {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.APIVersion == "" {
		t.Error("expected negotiated API version")
	}
}
