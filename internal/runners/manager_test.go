// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import "testing"

// fakeState records MarkConnected/MarkDisconnected calls so the manager's
// status wiring can be asserted without a database.
type fakeState struct {
	connected   map[uint]string // id -> last version reported
	disconnects map[uint]int
}

func newFakeState() *fakeState {
	return &fakeState{connected: map[uint]string{}, disconnects: map[uint]int{}}
}

func (f *fakeState) MarkConnected(id uint, _, _, version, _ string) { f.connected[id] = version }
func (f *fakeState) MarkDisconnected(id uint)                       { f.disconnects[id]++ }

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

// Disconnecting an unknown runner is a safe no-op (it never panics on a missing
// session), so a delete/disable of an offline runner is always safe to call.
func TestDisconnectUnknownIsNoop(t *testing.T) {
	m := NewManager(newFakeState())
	m.Disconnect(999) // must not panic
}
