// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodes

import (
	"sync"
	"testing"
	"time"
)

// recorder collects the transitions a listener is told about.
type recorder struct {
	mu   sync.Mutex
	seen []bool
	ch   chan struct{}
}

func newRecorder() *recorder { return &recorder{ch: make(chan struct{}, 16)} }

func (r *recorder) listen(_ uint, _ string, online bool) {
	r.mu.Lock()
	r.seen = append(r.seen, online)
	r.mu.Unlock()
	r.ch <- struct{}{}
}

// await waits for n FURTHER notifications and returns everything seen so far, so the assertions do
// not race the hooks' goroutines.
func (r *recorder) await(t *testing.T, n int) []bool {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-r.ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for notification %d of %d", i+1, n)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.seen...)
}

// quiet fails if any further notification arrives.
func (r *recorder) quiet(t *testing.T) {
	t.Helper()
	select {
	case <-r.ch:
		t.Fatal("a node was announced again in the state it was already announced in")
	case <-time.After(150 * time.Millisecond):
	}
}

func newStatusManager() *Manager {
	return &Manager{sessions: nil, online: map[uint]bool{}}
}

// The direct-node poller re-confirms every node on every tick. Listeners must hear the transitions,
// not the ticks: alerting would re-raise, and the console would redraw, on a node that never moved.
func TestNotify_OnlyOnTransition(t *testing.T) {
	m := newStatusManager()
	r := newRecorder()
	m.AddStatusListener(r.listen)

	m.notify(7, "web-1", true)
	if got := r.await(t, 1); len(got) != 1 || !got[0] {
		t.Fatalf("first observation must be announced, got %v", got)
	}
	m.notify(7, "web-1", true)
	m.notify(7, "web-1", true)
	r.quiet(t)

	m.notify(7, "web-1", false)
	if got := r.await(t, 1); len(got) != 2 || got[1] {
		t.Fatalf("going offline must be announced, got %v", got)
	}
}

// A node's very first observation is a transition whatever it is: after a control-plane restart the
// console holds no state at all, so an agent that is already up has to be announced.
func TestNotify_FirstObservationOffline(t *testing.T) {
	m := newStatusManager()
	r := newRecorder()
	m.AddStatusListener(r.listen)

	m.notify(3, "db-1", false)
	if got := r.await(t, 1); len(got) != 1 || got[0] {
		t.Fatalf("the first observation must be announced even when it is offline, got %v", got)
	}
}

// Every listener hears every transition: alerting and the console's live stream both watch.
func TestAddStatusListener_FansOut(t *testing.T) {
	m := newStatusManager()
	a, b := newRecorder(), newRecorder()
	m.AddStatusListener(a.listen)
	m.AddStatusListener(b.listen)

	m.notify(1, "n", true)
	a.await(t, 1)
	b.await(t, 1)
}

// Nodes are tracked apart: one going offline says nothing about another.
func TestNotify_PerNode(t *testing.T) {
	m := newStatusManager()
	r := newRecorder()
	m.AddStatusListener(r.listen)

	m.notify(1, "a", true)
	m.notify(2, "b", true)
	if got := r.await(t, 2); len(got) != 2 {
		t.Fatalf("both nodes must be announced, got %v", got)
	}
	m.notify(1, "a", true)
	r.quiet(t)
}

// A deleted node's id may later belong to a different machine, so its announced state goes with it.
func TestForgetStatus_ReannouncesAfterDelete(t *testing.T) {
	m := newStatusManager()
	r := newRecorder()
	m.AddStatusListener(r.listen)

	m.notify(5, "old", true)
	r.await(t, 1)
	m.ForgetStatus(5)
	m.notify(5, "new", true)
	if got := r.await(t, 1); len(got) != 2 {
		t.Fatalf("a forgotten node must be announced afresh, got %v", got)
	}
}
