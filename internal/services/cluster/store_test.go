// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type memStore struct {
	def    models.Cluster
	others map[uint]models.Cluster
}

func (m *memStore) FindDefault() (*models.Cluster, error) {
	c := m.def
	return &c, nil
}

func (m *memStore) List() ([]models.Cluster, error) {
	out := []models.Cluster{m.def}
	for _, c := range m.others {
		out = append(out, c)
	}
	return out, nil
}

func (m *memStore) IDByUID(string) (uint, error) { return 0, errors.New("not found") }

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

func (m *memStore) UpdateColumns(_ uint, cols map[string]any) error {
	for k, v := range cols {
		switch k {
		case "display_name":
			m.def.DisplayName = v.(string)
		case "location_code":
			m.def.LocationCode = v.(string)
		case "agent_token_hash":
			m.def.AgentTokenHash = v.(string)
		case "mode":
			m.def.Mode = v.(models.ClusterMode)
		}
	}
	return nil
}

type fakeRegistrar struct{ registered string }

func (f *fakeRegistrar) FindBySwarmNodeID(string) (*models.Server, error) {
	return nil, errors.New("not found")
}

func (f *fakeRegistrar) RegisterClusterNode(swarmNodeID, _ string) (*models.Server, error) {
	f.registered = swarmNodeID
	return &models.Server{ID: 9, SwarmNodeID: swarmNodeID}, nil
}

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

func TestOnlyTheDefaultClusterIsDrivenAsASwarm(t *testing.T) {
	fd := &fakeDocker{info: docker.SwarmInfo{LocalNodeState: "active", ControlAvailable: true, NodeID: "mgr1"}}
	store := &memStore{
		def: models.Cluster{ID: 1, IsDefault: true, Mode: models.ClusterModeSwarm},
		others: map[uint]models.Cluster{
			2: {ID: 2, Mode: models.ClusterModeStandalone, ManagerServerID: 7},
			3: {ID: 3, Mode: models.ClusterModeSwarm, ManagerServerID: 8},
		},
	}
	s := NewService(fakeClients{local: fd}, &fakeNodes{})
	s.SetStore(store)
	s.Refresh(context.Background())

	for id, want := range map[uint]bool{models.DefaultClusterID: true, 1: true, 2: false, 3: false} {
		if got := s.IsSwarm(id); got != want {
			t.Errorf("IsSwarm(%d) = %v, want %v", id, got, want)
		}
	}
	if mgr, err := s.Manager(context.Background(), 1); err != nil || mgr != fd {
		t.Errorf("default manager = %v, %v; want the local engine", mgr, err)
	}
	if _, err := s.Manager(context.Background(), 2); err != nil {
		t.Errorf("standalone manager err = %v, want its node's client", err)
	}
	if _, err := s.Manager(context.Background(), 3); !errors.Is(err, ErrRemoteSwarm) {
		t.Errorf("remote swarm err = %v, want ErrRemoteSwarm", err)
	}
	if _, err := s.Manager(context.Background(), 99); !errors.Is(err, ErrClusterNotFound) {
		t.Errorf("unknown cluster err = %v, want ErrClusterNotFound", err)
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

func TestAgentTokenIsCheckedAgainstTheDefaultCluster(t *testing.T) {
	fd := &fakeDocker{nodes: []docker.SwarmNode{{ID: "w1"}}}
	store := &memStore{def: models.Cluster{ID: 1, IsDefault: true, AgentTokenHash: hashClusterToken("mbc_secret")}}
	s := NewService(fakeClients{local: fd}, &fakeNodes{})
	s.SetStore(store)
	reg := &fakeRegistrar{}
	s.SetAgentDeps(reg, "", nil, "")

	srv, err := s.AuthenticateAgent(context.Background(), "mbc_secret", "w1", "host-1")
	if err != nil || srv.ID != 9 || reg.registered != "w1" {
		t.Fatalf("authenticate = %+v, %v; want the swarm worker registered", srv, err)
	}
	if _, err := s.AuthenticateAgent(context.Background(), "mbc_wrong", "w1", "host-1"); !errors.Is(err, ErrBadClusterToken) {
		t.Errorf("wrong token err = %v, want ErrBadClusterToken", err)
	}
}
