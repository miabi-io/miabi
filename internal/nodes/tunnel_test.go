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

package nodes

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jkaninda/wstunnel"
	"github.com/miabi-io/miabi/internal/docker"
)

func TestTunnelEndToEnd(t *testing.T) {
	// Stub Docker engine (what the agent pipes to).
	ping := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", "1.43")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}
	dockerMux := http.NewServeMux()
	dockerMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			ping(w, r)
		case strings.HasSuffix(r.URL.Path, "/info"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ServerVersion":"27.5.1","NCPU":8,"Containers":1}`))
		default:
			http.NotFound(w, r)
		}
	})
	dockerSrv := httptest.NewServer(dockerMux)
	defer dockerSrv.Close()
	dockerAddr := strings.TrimPrefix(dockerSrv.URL, "http://")

	clientCh := make(chan docker.Client, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		sess, err := wstunnel.Client(ws)
		if err != nil {
			return
		}
		dcli, err := docker.NewRemote(func(_ context.Context) (net.Conn, error) { return sess.OpenStream() })
		if err != nil {
			return
		}
		clientCh <- dcli
		<-sess.CloseChan() // keep the handler (and tunnel) alive
	}))
	defer cp.Close()

	wsURL := "ws://" + strings.TrimPrefix(cp.URL, "http://") + "/"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	defer ws.Close()
	sess, err := wstunnel.Server(ws)
	if err != nil {
		t.Fatalf("agent session: %v", err)
	}
	defer sess.Close()
	go func() {
		for {
			stream, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func(s net.Conn) {
				defer s.Close()
				d, err := net.Dial("tcp", dockerAddr)
				if err != nil {
					return
				}
				defer d.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(d, s); done <- struct{}{} }()
				go func() { _, _ = io.Copy(s, d); done <- struct{}{} }()
				<-done
			}(stream)
		}
	}()

	// The control plane handler produces the tunneled client; call Info through it.
	var dcli docker.Client
	select {
	case dcli = <-clientCh:
	case <-time.After(5 * time.Second):
		t.Fatal("control plane never produced a tunneled client")
	}
	defer dcli.Close()

	info, err := dcli.Info(context.Background())
	if err != nil {
		t.Fatalf("Info over tunnel: %v", err)
	}
	if info.Version != "27.5.1" || info.CPUs != 8 || info.Containers != 1 {
		t.Fatalf("unexpected info over tunnel: %+v", info)
	}
}
