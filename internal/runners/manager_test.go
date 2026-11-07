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

package runners

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/miabi-io/miabi/internal/models"
)

type fakeState struct {
	mu          sync.Mutex
	connected   map[uint]string // id -> last version reported
	disconnects map[uint]int
}

func newFakeState() *fakeState {
	return &fakeState{connected: map[uint]string{}, disconnects: map[uint]int{}}
}

func (f *fakeState) MarkConnected(id uint, facts models.RunnerFacts) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected[id] = facts.Version
}

func (f *fakeState) MarkDisconnected(id uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnects[id]++
}

func (f *fakeState) disconnectCount(id uint) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disconnects[id]
}

// A freshly-built manager reports every runner offline and hands back no session.
func TestManagerOfflineByDefault(t *testing.T) {
	m := NewManager(newFakeState())
	if m.Connected(7) {
		t.Error("runner 7 should be offline with no tunnel")
	}
	if _, ok := m.Session(7); ok {
		t.Error("no session should exist for an unconnected runner")
	}
}

// Lets deleting or disabling an offline runner always be safe.
func TestDisconnectUnknownIsNoop(t *testing.T) {
	m := NewManager(newFakeState())
	m.Disconnect(999) // must not panic
}

// testSession returns the control-plane side of an in-memory yamux tunnel.
func testSession(t *testing.T) *yamux.Session {
	t.Helper()
	a, b := net.Pipe()
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard
	sess, err := yamux.Client(a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := yamux.Server(b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(); _ = peer.Close() })
	return sess
}

type statusLog struct {
	mu     sync.Mutex
	events []bool
	ch     chan struct{}
}

func newStatusLog(m *Manager) *statusLog {
	l := &statusLog{ch: make(chan struct{}, 16)}
	m.AddStatusListener(func(id uint, online bool) {
		l.mu.Lock()
		l.events = append(l.events, online)
		l.mu.Unlock()
		l.ch <- struct{}{}
	})
	return l
}

func (l *statusLog) wait(t *testing.T, n int) []bool {
	t.Helper()
	for {
		l.mu.Lock()
		got := append([]bool{}, l.events...)
		l.mu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case <-l.ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("got status events %v, want %d", got, n)
		}
	}
}

func TestStatusListenerSeesConnectAndDisconnect(t *testing.T) {
	state := newFakeState()
	m := NewManager(state)
	events := newStatusLog(m)
	sess := testSession(t)

	done := make(chan struct{})
	go func() { m.serve(&models.Runner{ID: 3}, models.RunnerFacts{}, sess); close(done) }()
	if got := events.wait(t, 1); !got[0] || !m.Connected(3) {
		t.Fatalf("after connect: events %v, connected %v", got, m.Connected(3))
	}

	_ = sess.Close()
	<-done
	if got := events.wait(t, 2); got[1] || m.Connected(3) || state.disconnectCount(3) != 1 {
		t.Fatalf("after disconnect: events %v, connected %v, disconnects %d", got, m.Connected(3), state.disconnectCount(3))
	}
}

// A reconnect closes the superseded tunnel, which must not report the live runner offline.
func TestSupersededTunnelKeepsRunnerOnline(t *testing.T) {
	state := newFakeState()
	m := NewManager(state)
	events := newStatusLog(m)

	oldSess, newSess := testSession(t), testSession(t)
	oldDone := make(chan struct{})
	go func() { m.serve(&models.Runner{ID: 4}, models.RunnerFacts{}, oldSess); close(oldDone) }()
	events.wait(t, 1)
	go m.serve(&models.Runner{ID: 4}, models.RunnerFacts{}, newSess)
	<-oldDone

	got := events.wait(t, 2)
	if len(got) != 2 || !got[1] || !m.Connected(4) || state.disconnectCount(4) != 0 {
		t.Fatalf("events %v, connected %v, disconnects %d; want [true true], online, 0", got, m.Connected(4), state.disconnectCount(4))
	}
}
