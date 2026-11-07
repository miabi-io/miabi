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

package database

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// fakeServers answers the node-existence question; a node not in the set has been deleted.
type fakeServers struct{ exists map[uint]bool }

func (f fakeServers) Get(id uint) (*models.Server, error) {
	if f.exists[id] {
		return &models.Server{ID: id}, nil
	}
	return nil, errors.New("node not found")
}

// fakeClients satisfies NodeDocker for the local-id check.
type fakeClients struct{ local uint }

func (f fakeClients) For(uint) (docker.Client, error) { return nil, errors.New("offline") }
func (f fakeClients) LocalID() uint                   { return f.local }

func TestNodeGone(t *testing.T) {
	s := &Service{
		clients:    fakeClients{local: 1},
		serverInfo: fakeServers{exists: map[uint]bool{1: true, 2: true}},
	}

	if s.nodeGone(&models.DatabaseInstance{ServerID: 2}) {
		t.Error("a node that still exists must not read as gone")
	}
	if !s.nodeGone(&models.DatabaseInstance{ServerID: 9}) {
		t.Error("a deleted node must read as gone")
	}
	// The control-plane node is never deleted, so it never reads as gone even if the lookup fails.
	if s.nodeGone(&models.DatabaseInstance{ServerID: 1}) {
		t.Error("the local node must never read as gone")
	}
	// Unwired: assume the node is fine rather than destroy on a guess.
	if (&Service{}).nodeGone(&models.DatabaseInstance{ServerID: 9}) {
		t.Error("with no server lookup wired, nothing may be declared orphaned")
	}
}

// Stop needed a client the deleted node could never provide and Delete refused a running instance: a deadlock.
func TestDeleteGuardReleasesAnOrphanedInstance(t *testing.T) {
	gone := &Service{
		clients:    fakeClients{local: 1},
		serverInfo: fakeServers{exists: map[uint]bool{1: true}},
	}
	if gone.runningBlocksDelete(&models.DatabaseInstance{ServerID: 9, Status: models.DBStatusRunning}) {
		t.Error("an instance on a deleted node must be deletable")
	}

	// On a live node the guard stands: "stop it first" is sound advice there.
	live := &Service{
		clients:    fakeClients{local: 1},
		serverInfo: fakeServers{exists: map[uint]bool{1: true, 2: true}},
	}
	if !live.runningBlocksDelete(&models.DatabaseInstance{ServerID: 2, Status: models.DBStatusRunning}) {
		t.Error("a running instance on a live node must still refuse deletion")
	}
	// And a stopped instance is deletable either way.
	if live.runningBlocksDelete(&models.DatabaseInstance{ServerID: 2, Status: models.DBStatusStopped}) {
		t.Error("a stopped instance must be deletable")
	}
}

// A deleted node must not leave the console showing "running" for something with no engine.
func TestLiveStatusReportsAnOrphanedInstanceStopped(t *testing.T) {
	s := &Service{
		clients:    fakeClients{local: 1},
		serverInfo: fakeServers{exists: map[uint]bool{1: true}},
	}
	inst := &models.DatabaseInstance{ServerID: 9, Status: models.DBStatusRunning, ContainerID: "abc"}

	ls := s.LiveStatus(t.Context(), inst)
	if ls.Running || ls.HasContainer {
		t.Fatalf("orphaned instance reported running=%v hasContainer=%v", ls.Running, ls.HasContainer)
	}
	if !ls.NodeMissing {
		t.Error("NodeMissing must be set so the console can say why")
	}
	if ls.Status != string(models.DBStatusStopped) {
		t.Errorf("status = %q, want stopped", ls.Status)
	}
	// The stored status is still reported alongside, so nothing pretends the row was edited.
	if ls.StoredStatus != models.DBStatusRunning {
		t.Errorf("stored status = %q, want the untouched running", ls.StoredStatus)
	}
}
