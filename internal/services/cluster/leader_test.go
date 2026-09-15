// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// A standalone worker refreshes swarm state for its own deploys, but used to write what it saw as
// well, making it a second writer beside the control plane.
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
