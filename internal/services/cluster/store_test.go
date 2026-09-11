// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type memStore struct {
	def       models.Cluster
	others    map[uint]models.Cluster
	workloads map[uint]int64 // by server id
	assigned  map[uint]uint  // server id -> cluster id
}

func (m *memStore) all() []models.Cluster {
	out := []models.Cluster{m.def}
	for _, c := range m.others {
		out = append(out, c)
	}
	return out
}

func (m *memStore) List() ([]models.Cluster, error) { return m.all(), nil }

func (m *memStore) FindDefault() (*models.Cluster, error) {
	c := m.def
	return &c, nil
}

func (m *memStore) FindByID(id uint) (*models.Cluster, error) {
	if id == models.DefaultClusterID || id == m.def.ID {
		return m.FindDefault()
	}
	c, ok := m.others[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return &c, nil
}

func (m *memStore) FindByName(name string) (*models.Cluster, error) {
	for _, c := range m.all() {
		if c.Name == name {
			return &c, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *memStore) FindByAgentTokenHash(hash string) (*models.Cluster, error) {
	for _, c := range m.all() {
		if hash != "" && c.AgentTokenHash == hash {
			return &c, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *memStore) IDByUID(string) (uint, error) { return 0, errors.New("not found") }

func (m *memStore) UpdateColumns(id uint, cols map[string]any) error {
	c, err := m.FindByID(id)
	if err != nil {
		return err
	}
	for k, v := range cols {
		switch k {
		case "display_name":
			c.DisplayName = v.(string)
		case "location_code":
			c.LocationCode = v.(string)
		case "agent_token_hash":
			c.AgentTokenHash = v.(string)
		case "mode":
			c.Mode = v.(models.ClusterMode)
		case "ingress_server_id":
			c.IngressServerID = v.(uint)
		case "ingress_ip":
			c.IngressIP = v.(string)
		case "ingress_hostname":
			c.IngressHostname = v.(string)
		case "visibility":
			c.Visibility = v.(models.ClusterVisibility)
		case "cordoned":
			c.Cordoned = v.(bool)
		}
	}
	if c.ID == m.def.ID {
		m.def = *c
	} else {
		m.others[c.ID] = *c
	}
	return nil
}

func (m *memStore) CountWorkloads(clusterID uint) (int64, error) {
	var n int64
	for serverID, cid := range m.assigned {
		if cid == clusterID {
			n += m.workloads[serverID]
		}
	}
	return n, nil
}

func (m *memStore) CountServerWorkloads(serverID uint) (int64, error) {
	return m.workloads[serverID], nil
}

func (m *memStore) CreateStandalone(srv *models.Server, name string) (*models.Cluster, error) {
	c := models.Cluster{ID: 100 + uint(len(m.others)), Name: name, Mode: models.ClusterModeStandalone, ManagerServerID: srv.ID}
	if m.others == nil {
		m.others = map[uint]models.Cluster{}
	}
	m.others[c.ID] = c
	srv.ClusterID = c.ID
	return &c, m.AssignServer(srv.ID, c.ID)
}

func (m *memStore) AssignServer(serverID, clusterID uint) error {
	if m.assigned == nil {
		m.assigned = map[uint]uint{}
	}
	m.assigned[serverID] = clusterID
	return nil
}

// multiClients hands out a separate fake engine per remote node, unlike fakeClients.
type multiClients struct {
	local  *fakeDocker
	remote map[uint]*fakeDocker
}

func (m multiClients) For(id uint) (docker.Client, error) {
	if id == 0 || id == 1 {
		return m.local, nil
	}
	if d, ok := m.remote[id]; ok {
		return d, nil
	}
	return nil, errors.New("node is offline")
}
func (m multiClients) Local() docker.Client { return m.local }
func (m multiClients) LocalID() uint        { return 1 }

type fakeRegistrar struct {
	registered string
	clusterID  uint
}

func (f *fakeRegistrar) FindBySwarmNodeID(string) (*models.Server, error) {
	return nil, errors.New("not found")
}

func (f *fakeRegistrar) RegisterClusterNode(clusterID uint, swarmNodeID, _ string) (*models.Server, error) {
	f.registered, f.clusterID = swarmNodeID, clusterID
	return &models.Server{ID: 9, SwarmNodeID: swarmNodeID, ClusterID: clusterID}, nil
}

var activeManager = docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true}

func TestRefreshRecordsTheDefaultClusterMode(t *testing.T) {
	fd := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true, NodeID: "mgr1"}}
	store := &memStore{def: models.Cluster{ID: 1, IsDefault: true, Mode: models.ClusterModeStandalone}}
	s := NewService(fakeClients{local: fd}, &fakeNodes{})
	s.SetStore(store)

	s.Refresh(context.Background())
	if store.def.Mode != models.ClusterModeSwarm {
		t.Fatalf("mode = %q after the engine became a swarm manager, want swarm", store.def.Mode)
	}

	fd.info = docker.SwarmInfo{LocalNodeState: "inactive"}
	s.Refresh(context.Background())
	if store.def.Mode != models.ClusterModeStandalone {
		t.Errorf("mode = %q after the engine left the swarm, want standalone", store.def.Mode)
	}
}

func TestSwarmStateIsKeptPerCluster(t *testing.T) {
	remote := &fakeDocker{info: activeManager}
	standaloneNode := &fakeDocker{}
	clients := multiClients{
		local:  &fakeDocker{info: activeManager},
		remote: map[uint]*fakeDocker{7: remote, 8: standaloneNode},
	}
	store := &memStore{
		def: models.Cluster{ID: 1, IsDefault: true, Mode: models.ClusterModeSwarm},
		others: map[uint]models.Cluster{
			2: {ID: 2, Mode: models.ClusterModeStandalone, ManagerServerID: 8},
			3: {ID: 3, Mode: models.ClusterModeSwarm, ManagerServerID: 7},
		},
	}
	s := NewService(clients, &fakeNodes{})
	s.SetStore(store)
	s.Refresh(context.Background())

	for id, want := range map[uint]bool{models.DefaultClusterID: true, 1: true, 2: false, 3: true} {
		if got := s.IsSwarm(id); got != want {
			t.Errorf("IsSwarm(%d) = %v, want %v", id, got, want)
		}
	}
	if mgr, err := s.Manager(context.Background(), 3); err != nil || mgr != remote {
		t.Errorf("remote swarm manager = %v, %v; want its manager node's engine", mgr, err)
	}
	if mgr, err := s.Manager(context.Background(), 2); err != nil || mgr != standaloneNode {
		t.Errorf("standalone manager = %v, %v; want the node's engine", mgr, err)
	}
	if _, err := s.Manager(context.Background(), 99); !errors.Is(err, ErrClusterNotFound) {
		t.Errorf("unknown cluster err = %v, want ErrClusterNotFound", err)
	}

	remote.info = docker.SwarmInfo{LocalNodeState: "inactive"}
	s.Refresh(context.Background())
	if s.IsSwarm(3) {
		t.Error("a remote swarm whose manager left is still reported as a swarm")
	}
}

func TestEnablingARemoteSwarmInitializesItOnTheNode(t *testing.T) {
	node8 := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "inactive"}}
	store := &memStore{
		def:       models.Cluster{ID: 1, IsDefault: true},
		others:    map[uint]models.Cluster{2: {ID: 2, Name: "paris", Mode: models.ClusterModeStandalone, ManagerServerID: 8}},
		workloads: map[uint]int64{8: 1},
	}
	nodes := &fakeNodes{servers: []models.Server{{ID: 8, ClusterID: 2}}}
	s := NewService(multiClients{local: &fakeDocker{}, remote: map[uint]*fakeDocker{8: node8}}, nodes)
	s.SetStore(store)

	if _, err := s.Enable(context.Background(), 2, "10.0.0.8"); !errors.Is(err, ErrNodeHasWorkloads) {
		t.Fatalf("enable on a node with workloads err = %v, want ErrNodeHasWorkloads", err)
	}
	if node8.initCalled {
		t.Fatal("a swarm was initialized on a node that still has workloads")
	}

	store.workloads = nil
	status, err := s.Enable(context.Background(), 2, "10.0.0.8")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !node8.initCalled || node8.initAddr != "10.0.0.8" {
		t.Errorf("swarm init on the node: called=%v addr=%q", node8.initCalled, node8.initAddr)
	}
	if got := store.others[2]; got.Mode != models.ClusterModeSwarm || got.IngressServerID != 8 {
		t.Errorf("cluster = %+v, want a swarm served by node 8", got)
	}
	if !status.Enabled || !s.IsSwarm(2) {
		t.Errorf("status.Enabled=%v IsSwarm=%v, want both true", status.Enabled, s.IsSwarm(2))
	}
	if len(node8.labelled) == 0 || nodes.servers[0].SwarmNodeID != "mgr1" {
		t.Errorf("labelled=%v swarm id=%q; want the manager labelled and recorded", node8.labelled, nodes.servers[0].SwarmNodeID)
	}
	if want := "miabi-ingress/mb-node-gateway"; !slices.Contains(node8.connected, want) {
		t.Errorf("connected = %v, want the ingress node's gateway on the cluster's ingress overlay", node8.connected)
	}
}

func TestJoiningARemoteSwarmMovesTheNodeIntoTheCluster(t *testing.T) {
	manager := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true, NodeAddr: "10.0.0.7"}}
	joiner := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "inactive"}}
	store := &memStore{
		def: models.Cluster{ID: 1, IsDefault: true},
		others: map[uint]models.Cluster{
			3: {ID: 3, Mode: models.ClusterModeSwarm, ManagerServerID: 7},
			4: {ID: 4, Mode: models.ClusterModeStandalone, ManagerServerID: 9},
		},
		workloads: map[uint]int64{9: 2},
	}
	nodes := &fakeNodes{servers: []models.Server{{ID: 7, ClusterID: 3}, {ID: 9, ClusterID: 4}}}
	s := NewService(multiClients{local: &fakeDocker{}, remote: map[uint]*fakeDocker{7: manager, 9: joiner}}, nodes)
	s.SetStore(store)
	s.Refresh(context.Background())

	if err := s.JoinNode(context.Background(), 3, 9); !errors.Is(err, ErrNodeHasWorkloads) {
		t.Fatalf("join with workloads err = %v, want ErrNodeHasWorkloads", err)
	}
	store.workloads = nil
	if err := s.JoinNode(context.Background(), 3, 9); err != nil {
		t.Fatalf("join: %v", err)
	}
	if joiner.joinReq == nil || joiner.joinReq.RemoteAddrs[0] != "10.0.0.7:2377" || joiner.joinReq.JoinToken != "SWMTKN-worker" {
		t.Errorf("join request = %+v, want the remote manager's address and worker token", joiner.joinReq)
	}
	if store.assigned[9] != 3 {
		t.Errorf("node 9 assigned to cluster %d, want 3", store.assigned[9])
	}
	if err := s.JoinNode(context.Background(), 3, 7); !errors.Is(err, ErrManagerNode) {
		t.Errorf("joining the cluster's own manager err = %v, want ErrManagerNode", err)
	}
}

func TestAgentTokenNamesItsCluster(t *testing.T) {
	manager := &fakeDocker{info: activeManager, nodes: []docker.SwarmNode{{ID: "w1"}}}
	store := &memStore{
		def: models.Cluster{ID: 1, IsDefault: true, AgentTokenHash: hashClusterToken("mbc_default")},
		others: map[uint]models.Cluster{
			3: {ID: 3, Mode: models.ClusterModeSwarm, ManagerServerID: 7, AgentTokenHash: hashClusterToken("mbc_remote")},
		},
	}
	s := NewService(multiClients{local: &fakeDocker{}, remote: map[uint]*fakeDocker{7: manager}}, &fakeNodes{})
	s.SetStore(store)
	reg := &fakeRegistrar{}
	s.SetAgentDeps(reg, "", nil, "")

	srv, err := s.AuthenticateAgent(context.Background(), "mbc_remote", "w1", "host-1")
	if err != nil || srv.ID != 9 || reg.clusterID != 3 {
		t.Fatalf("authenticate = %+v, %v (cluster %d); want w1 registered into cluster 3", srv, err, reg.clusterID)
	}
	if _, err := s.AuthenticateAgent(context.Background(), "mbc_default", "w1", "host-1"); !errors.Is(err, ErrNotSwarmMember) {
		t.Errorf("default token for a remote member err = %v, want ErrNotSwarmMember", err)
	}
	if _, err := s.AuthenticateAgent(context.Background(), "mbc_wrong", "w1", "host-1"); !errors.Is(err, ErrBadClusterToken) {
		t.Errorf("unknown token err = %v, want ErrBadClusterToken", err)
	}
}

func TestWorkspaceNetworksAreOverlaysInARemoteSwarm(t *testing.T) {
	manager := &fakeDocker{info: activeManager}
	store := &memStore{
		def:    models.Cluster{ID: 1, IsDefault: true, Mode: models.ClusterModeSwarm},
		others: map[uint]models.Cluster{3: {ID: 3, Mode: models.ClusterModeSwarm, ManagerServerID: 7}},
	}
	local := &fakeDocker{info: activeManager}
	s := NewService(multiClients{local: local, remote: map[uint]*fakeDocker{7: manager}}, &fakeNodes{})
	s.SetStore(store)
	s.Refresh(context.Background())

	bridge := models.Network{DockerName: "mb-ws-4", Driver: "bridge", Internal: true}
	if s.WorkspaceOverlay(1, bridge) {
		t.Error("a bridge record was treated as an overlay in the default cluster")
	}
	if !s.WorkspaceOverlay(3, bridge) {
		t.Error("a workspace network is not an overlay in a remote swarm")
	}
	if err := s.EnsureWorkspaceOverlay(context.Background(), 3, bridge); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(manager.ensured) != 1 {
		t.Fatalf("ensured on the remote manager = %+v, want one overlay", manager.ensured)
	}
	if got := manager.ensured[0]; got.Name != "mb-ws-4" || got.Driver != "overlay" || !got.Attachable || !got.Internal {
		t.Errorf("overlay spec = %+v, want an attachable internal overlay named after the record", got)
	}
	if err := s.EnsureWorkspaceOverlay(context.Background(), 1, bridge); err != nil || len(local.ensured) != 0 {
		t.Errorf("the default cluster's network was created here (err %v, %+v); that is the network service's job", err, local.ensured)
	}
}

func TestUpdateClusterValidatesTheLocation(t *testing.T) {
	store := &memStore{def: models.Cluster{ID: 1, IsDefault: true, Name: "default"}}
	s := &Service{store: store}

	bad := "EU Central"
	if _, err := s.UpdateCluster(1, ClusterPatch{LocationCode: &bad}); !errors.Is(err, ErrInvalidLocationCode) {
		t.Fatalf("err = %v, want ErrInvalidLocationCode", err)
	}
	name, code := " Frankfurt ", "eu-central"
	c, err := s.UpdateCluster(models.DefaultClusterID, ClusterPatch{DisplayName: &name, LocationCode: &code})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if c.DisplayName != "Frankfurt" || c.LocationCode != "eu-central" {
		t.Errorf("cluster = %+v, want Frankfurt · eu-central", c)
	}
	if _, err := s.UpdateCluster(42, ClusterPatch{DisplayName: &name}); !errors.Is(err, ErrClusterNotFound) {
		t.Errorf("unknown cluster err = %v, want ErrClusterNotFound", err)
	}
}
