// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

// fakeDocker stubs the swarm surface of docker.Client; embedding the interface
// satisfies the unused methods (which the cluster service never calls here).
type fakeDocker struct {
	docker.Client
	info         docker.SwarmInfo
	nodes        []docker.SwarmNode
	initCalled   bool
	initAddr     string
	overlaysMade []string
}

func (f *fakeDocker) Swarm(context.Context) (docker.SwarmInfo, error)        { return f.info, nil }
func (f *fakeDocker) SwarmNodes(context.Context) ([]docker.SwarmNode, error) { return f.nodes, nil }
func (f *fakeDocker) CreateOverlayNetwork(_ context.Context, name string) (string, error) {
	f.overlaysMade = append(f.overlaysMade, name)
	return name, nil
}
func (f *fakeDocker) SwarmInit(_ context.Context, req docker.SwarmInitRequest) (string, error) {
	f.initCalled = true
	f.initAddr = req.AdvertiseAddr
	// Simulate the engine entering swarm mode as a manager.
	f.info = docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true, NodeID: "mgr1", NodeAddr: req.AdvertiseAddr, Managers: 1, Nodes: 1}
	return "mgr1", nil
}

type fakeClients struct{ local *fakeDocker }

func (f fakeClients) For(uint) (docker.Client, error) { return f.local, nil }
func (f fakeClients) Local() docker.Client            { return f.local }
func (f fakeClients) LocalID() uint                   { return 1 }

type fakeNodes struct{ servers []models.Server }

func (f *fakeNodes) List(context.Context) ([]models.Server, error) { return f.servers, nil }
func (f *fakeNodes) Get(id uint) (*models.Server, error) {
	for i := range f.servers {
		if f.servers[i].ID == id {
			return &f.servers[i], nil
		}
	}
	return nil, errors.New("not found")
}
func (f *fakeNodes) SetSwarmNodeID(id uint, swarmNodeID string) error {
	for i := range f.servers {
		if f.servers[i].ID == id {
			f.servers[i].SwarmNodeID = swarmNodeID
			return nil
		}
	}
	return nil
}

func TestCapClusterFalseOnPlainDocker(t *testing.T) {
	fd := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "inactive"}}
	s := NewService(fakeClients{local: fd}, &fakeNodes{})
	s.Refresh(context.Background())
	if s.CapCluster() {
		t.Fatal("expected CapCluster=false on inactive engine")
	}
	if got := s.Status().LocalNodeState; got != "inactive" {
		t.Fatalf("status local_node_state = %q, want inactive", got)
	}
}

func TestCapClusterTrueWhenManager(t *testing.T) {
	fd := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true, NodeID: "mgr1"}}
	nodes := &fakeNodes{servers: []models.Server{{ID: 1, IsLocal: true}}}
	s := NewService(fakeClients{local: fd}, nodes)
	s.Refresh(context.Background())
	if !s.CapCluster() {
		t.Fatal("expected CapCluster=true for a reachable manager")
	}
	// The manager's swarm node id is persisted for correlation.
	if nodes.servers[0].SwarmNodeID != "mgr1" {
		t.Fatalf("manager swarm node id = %q, want mgr1", nodes.servers[0].SwarmNodeID)
	}
}

func TestEnrichRolesAndStandalone(t *testing.T) {
	fd := &fakeDocker{
		info: docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true, NodeID: "mgr1"},
		nodes: []docker.SwarmNode{
			{ID: "mgr1", Role: "manager", Leader: true, Availability: "active", State: "ready"},
			{ID: "w1", Role: "worker", Availability: "active", State: "ready"},
		},
	}
	servers := []models.Server{
		{ID: 1, IsLocal: true, SwarmNodeID: "mgr1"},
		{ID: 2, SwarmNodeID: "w1"},
		{ID: 3}, // no swarm id → standalone
	}
	s := NewService(fakeClients{local: fd}, &fakeNodes{servers: servers})
	s.Refresh(context.Background())
	s.Enrich(servers)

	if servers[0].SwarmRole != "leader" || !servers[0].InSwarm {
		t.Fatalf("manager: role=%q in_swarm=%v, want leader/true", servers[0].SwarmRole, servers[0].InSwarm)
	}
	if servers[1].SwarmRole != "worker" || !servers[1].InSwarm {
		t.Fatalf("worker: role=%q in_swarm=%v, want worker/true", servers[1].SwarmRole, servers[1].InSwarm)
	}
	if servers[2].SwarmRole != "standalone" || servers[2].InSwarm {
		t.Fatalf("orphan: role=%q in_swarm=%v, want standalone/false", servers[2].SwarmRole, servers[2].InSwarm)
	}
}

func TestEnableRequiresAdvertiseAddr(t *testing.T) {
	fd := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "inactive"}}
	s := NewService(fakeClients{local: fd}, &fakeNodes{})
	if _, err := s.Enable(context.Background(), "", ""); !errors.Is(err, ErrAdvertiseAddrRequired) {
		t.Fatalf("Enable(\"\") error = %v, want ErrAdvertiseAddrRequired", err)
	}
	if fd.initCalled {
		t.Fatal("swarm init must not run without an advertise address")
	}
}

func TestEnableInitializesSwarm(t *testing.T) {
	fd := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "inactive"}}
	nodes := &fakeNodes{servers: []models.Server{{ID: 1, IsLocal: true}}}
	s := NewService(fakeClients{local: fd}, nodes)
	status, err := s.Enable(context.Background(), "10.0.0.1", "prod-eu-west-1")
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !fd.initCalled || fd.initAddr != "10.0.0.1" {
		t.Fatalf("swarm init: called=%v addr=%q, want true/10.0.0.1", fd.initCalled, fd.initAddr)
	}
	if !status.Enabled || !s.CapCluster() {
		t.Fatalf("after enable: status.Enabled=%v CapCluster=%v, want true/true", status.Enabled, s.CapCluster())
	}
	// Enabling cluster mode pre-creates the shared ingress overlay so a gateway —
	// Miabi-managed or user-attached — has a network to join immediately, and the
	// status advertises its name for the "attach your own proxy" hint.
	if len(fd.overlaysMade) != 1 || fd.overlaysMade[0] != node.IngressOverlay {
		t.Fatalf("ingress overlay not pre-created: got %v, want [%s]", fd.overlaysMade, node.IngressOverlay)
	}
	if status.IngressNetwork != node.IngressOverlay {
		t.Fatalf("status.IngressNetwork = %q, want %q", status.IngressNetwork, node.IngressOverlay)
	}
}
