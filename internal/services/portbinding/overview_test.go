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

package portbinding

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func states(entries []PortEntry) map[int]string {
	out := map[int]string{}
	for _, e := range entries {
		out[e.HostPort] = e.State
	}
	return out
}

// An admin needs both disagreements named rather than averaged.
func TestReconcileNamesBothDisagreements(t *testing.T) {
	s := &Service{}
	bindings := []models.PortBinding{
		{ID: 1, HostPort: 8080, Protocol: "tcp", Status: models.PortBindingApproved},
		{ID: 2, HostPort: 8081, Protocol: "tcp", Status: models.PortBindingApproved},
		{ID: 3, HostPort: 8082, Protocol: "tcp", Status: models.PortBindingPending},
	}
	live := map[string]string{
		"8080/tcp": "acme-web",   // approved and actually running
		"9000/tcp": "some-other", // nobody's binding — the invisible case
	}

	got := states(s.reconcile(bindings, live, true))
	want := map[int]string{
		8080: StatePublished, // approved + live
		8081: StateReserved,  // approved, app not redeployed
		8082: StatePending,   // awaiting review, never published
		9000: StateUnmanaged, // holds the port, no binding
	}
	for port, state := range want {
		if got[port] != state {
			t.Errorf("port %d = %q, want %q", port, got[port], state)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d entries, want %d: %v", len(got), len(want), got)
	}
}

// Pending is never published, whatever a same-named container does on that port.
func TestPendingIsNeverPublished(t *testing.T) {
	s := &Service{}
	entries := s.reconcile(
		[]models.PortBinding{{ID: 1, HostPort: 8080, Protocol: "tcp", Status: models.PortBindingPending}},
		map[string]string{"8080/tcp": "squatter"}, true)

	if entries[0].State != StatePending {
		t.Errorf("state = %q, want %q", entries[0].State, StatePending)
	}
	// The conflict is still worth showing: this is why the request cannot be approved.
	if entries[0].Container != "squatter" {
		t.Errorf("container = %q, want the live owner named", entries[0].Container)
	}
}

// The live map is deliberately non-empty: the inspected flag, not map emptiness, must decide.
func TestUninspectedNodeClaimsNothing(t *testing.T) {
	s := &Service{}
	stale := map[string]string{"8080/tcp": "acme-web", "9000/tcp": "some-other"}
	entries := s.reconcile(
		[]models.PortBinding{{ID: 1, HostPort: 8080, Protocol: "tcp", Status: models.PortBindingApproved}},
		stale, false)

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 — an uninspected node invents no unmanaged ports: %v",
			len(entries), states(entries))
	}
	if entries[0].State != StateReserved {
		t.Errorf("state = %q, want %q: published cannot be claimed without asking the node",
			entries[0].State, StateReserved)
	}
}

func TestEntriesSortByPortThenProtocol(t *testing.T) {
	s := &Service{}
	entries := s.reconcile([]models.PortBinding{
		{ID: 1, HostPort: 9000, Protocol: "tcp", Status: models.PortBindingApproved},
		{ID: 2, HostPort: 8080, Protocol: "udp", Status: models.PortBindingApproved},
		{ID: 3, HostPort: 8080, Protocol: "tcp", Status: models.PortBindingApproved},
	}, nil, true)

	got := make([]string, len(entries))
	for i, e := range entries {
		got[i] = portKey(e.HostPort, e.Protocol)
	}
	want := []string{"8080/tcp", "8080/udp", "9000/tcp"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestParsePortKeyRoundTrips(t *testing.T) {
	for _, c := range []struct {
		port  int
		proto string
	}{{8080, "tcp"}, {53, "udp"}, {65535, "tcp"}} {
		p, pr := parsePortKey(portKey(c.port, c.proto))
		if p != c.port || pr != c.proto {
			t.Errorf("round trip %d/%s -> %d/%s", c.port, c.proto, p, pr)
		}
	}
}
