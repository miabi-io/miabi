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

package cluster

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// The boot refresh runs before the manager's agent reconnects and caches the cluster as down.
func TestIsSwarmRechecksAStaleMissOnAStoredSwarmCluster(t *testing.T) {
	mgr := &fakeDocker{}
	s := NewService(fakeClients{local: mgr}, &fakeNodes{})
	s.SetStore(&memStore{
		def:    models.Cluster{ID: 1, IsDefault: true, Name: "default"},
		others: map[uint]models.Cluster{4: {ID: 4, Name: "paris", Mode: models.ClusterModeSwarm, ManagerServerID: 9}},
	})

	if s.IsSwarm(4) {
		t.Fatal("IsSwarm = true while the manager is not a swarm manager")
	}
	mgr.info = docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true, NodeID: "m1"}
	if s.IsSwarm(4) {
		t.Fatal("recheck ran again inside the rate-limit window")
	}

	s.mu.Lock()
	s.rechecked[4] = time.Now().Add(-recheckInterval)
	s.mu.Unlock()
	if !s.IsSwarm(4) {
		t.Fatal("IsSwarm = false after the manager came back")
	}
	mgr.info = docker.SwarmInfo{}
	if !s.IsSwarm(4) {
		t.Fatal("the rechecked state was not cached until the next refresh")
	}
}

func TestIsSwarmDoesNotRecheckStandaloneClusters(t *testing.T) {
	mgr := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true}}
	s := NewService(fakeClients{local: mgr}, &fakeNodes{})
	s.SetStore(&memStore{
		def:    models.Cluster{ID: 1, IsDefault: true, Name: "default"},
		others: map[uint]models.Cluster{5: {ID: 5, Name: "edge", Mode: models.ClusterModeStandalone}},
	})
	if s.IsSwarm(5) || s.IsSwarm(models.DefaultClusterID) {
		t.Fatal("a standalone or default cluster was promoted to swarm by the recheck")
	}
}
