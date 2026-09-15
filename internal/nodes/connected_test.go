// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodes

import (
	"testing"
	"time"
)

func TestConnectedSince(t *testing.T) {
	c := NewClients(1, &stubClient{})

	if at, ok := c.ConnectedSince(1); !ok || !at.IsZero() {
		t.Fatalf("local ConnectedSince = (%v, %v); want (zero, true)", at, ok)
	}
	if _, ok := c.ConnectedSince(2); ok {
		t.Fatal("a node with no client reports connected")
	}

	before := time.Now()
	c.SetRemote(2, &stubClient{})
	if at, ok := c.ConnectedSince(2); !ok || at.Before(before) {
		t.Fatalf("ConnectedSince after connect = (%v, %v); want a time from the connect", at, ok)
	}

	c.RemoveRemote(2)
	if _, ok := c.ConnectedSince(2); ok {
		t.Fatal("a disconnected node still reports connected")
	}
}
