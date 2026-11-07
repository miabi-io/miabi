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
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type fakeLeader bool

func (l fakeLeader) Leading() bool { return bool(l) }

// Regression: a standalone worker wrote what it saw, becoming a second writer beside the control plane.
func TestRefreshWritesOnlyWhileLeading(t *testing.T) {
	fd := &fakeDocker{
		info: docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true, NodeID: "mgr1"},
		nodes: []docker.SwarmNode{
			{ID: "mgr1", Role: "manager"},
			{ID: "w1", Role: "worker", EngineVersion: "28.1.0"},
		},
	}
	nodes := &fakeNodes{servers: []models.Server{{ID: 1, IsLocal: true}, {ID: 2, SwarmNodeID: "w1"}}}
	s := NewService(fakeClients{local: fd}, nodes)

	s.SetLeader(fakeLeader(false))
	s.Refresh(context.Background())
	if !s.CapCluster() {
		t.Fatal("a refresh without leadership did not update the swarm state")
	}
	if nodes.servers[0].SwarmNodeID != "" || nodes.servers[1].EngineVersion != "" {
		t.Fatalf("a refresh without leadership wrote swarm_node_id=%q engine_version=%q",
			nodes.servers[0].SwarmNodeID, nodes.servers[1].EngineVersion)
	}

	s.SetLeader(fakeLeader(true))
	s.Refresh(context.Background())
	if nodes.servers[0].SwarmNodeID != "mgr1" || nodes.servers[1].EngineVersion != "28.1.0" {
		t.Fatalf("a leading refresh wrote swarm_node_id=%q engine_version=%q; want mgr1 and 28.1.0",
			nodes.servers[0].SwarmNodeID, nodes.servers[1].EngineVersion)
	}
}
