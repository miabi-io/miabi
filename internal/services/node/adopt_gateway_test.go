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

package node

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// clusterRow stands in for models.Cluster under sqlite, whose uid default is Postgres-only.
type clusterRow struct {
	ID              uint `gorm:"primaryKey"`
	UID             string
	Name            string
	Mode            string
	IsDefault       bool
	ManagerServerID uint
	IngressServerID uint
}

func (clusterRow) TableName() string { return "clusters" }

// Regression: adopting a gateway skipped the connectivity check, letting a default-cluster node get its own gateway.
func TestAdoptGatewayChecksConnectivity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&nameRow{}, &clusterRow{}); err != nil {
		t.Fatal(err)
	}
	clusters := []clusterRow{
		{ID: 1, Name: "default", Mode: string(models.ClusterModeSwarm), IsDefault: true},
		{ID: 2, Name: "eu", Mode: string(models.ClusterModeSwarm)},
	}
	if err := db.Create(&clusters).Error; err != nil {
		t.Fatal(err)
	}
	servers := repositories.NewServerRepository(db)
	s := NewService(servers, nil)

	member := func(name string, clusterID uint) *models.Server {
		t.Helper()
		srv, _, err := s.CreateNode(NodeInput{DisplayName: name})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		srv.ClusterID, srv.Connectivity = clusterID, models.ConnectivityCluster
		if err := servers.Update(srv); err != nil {
			t.Fatal(err)
		}
		return srv
	}
	inDefault := member("Paris", 1)
	inSwarm := member("Berlin", 2)
	s.SetClusters(repositories.NewClusterRepository(db))

	if _, err := s.AdoptGateway(inDefault.ID, "goma", "jkaninda/goma-gateway", ""); !errors.Is(err, ErrEdgeGatewayInDefaultCluster) {
		t.Fatalf("adopt on a default-cluster node = %v, want ErrEdgeGatewayInDefaultCluster", err)
	}
	unchanged, err := servers.FindByID(inDefault.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Connectivity != models.ConnectivityCluster || unchanged.GatewayImported {
		t.Errorf("a refused adopt changed the node: connectivity %q, imported %v", unchanged.Connectivity, unchanged.GatewayImported)
	}

	adopted, err := s.AdoptGateway(inSwarm.ID, "goma", "jkaninda/goma-gateway", "")
	if err != nil {
		t.Fatalf("adopt on a swarm member: %v", err)
	}
	if adopted.Connectivity != models.ConnectivityEdgeGateway || !adopted.GatewayImported || adopted.GatewayContainer != "goma" {
		t.Errorf("adopt = connectivity %q, imported %v, container %q; want edge-gateway, imported, goma",
			adopted.Connectivity, adopted.GatewayImported, adopted.GatewayContainer)
	}
}

// The gateway node must keep its gateway; another member may run one as an admin-chosen additional gateway.
func TestSwarmMemberConnectivityFollowsTheGatewayNode(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&nameRow{}, &clusterRow{}); err != nil {
		t.Fatal(err)
	}
	servers := repositories.NewServerRepository(db)
	s := NewService(servers, nil)
	node := func(name, swarmID string, c models.ServerConnectivity) *models.Server {
		t.Helper()
		srv, _, err := s.CreateNode(NodeInput{DisplayName: name})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		srv.ClusterID, srv.SwarmNodeID, srv.Connectivity = 2, swarmID, c
		if err := servers.Update(srv); err != nil {
			t.Fatal(err)
		}
		return srv
	}
	gateway := node("Gateway", "n1", models.ConnectivityEdgeGateway)
	member := node("Member", "n2", models.ConnectivityCluster)
	joining := node("Joining", "", models.ConnectivityCluster)
	if err := db.Create(&clusterRow{ID: 2, Name: "eu", Mode: string(models.ClusterModeSwarm), ManagerServerID: gateway.ID}).Error; err != nil {
		t.Fatal(err)
	}
	s.SetClusters(repositories.NewClusterRepository(db))

	extra, err := s.SetConnectivity(member.ID, models.ConnectivityEdgeGateway)
	if err != nil || !extra.AdditionalGateway {
		t.Fatalf("edge gateway on a non-gateway member = %+v, %v; want an additional gateway", extra, err)
	}
	if back, err := s.SetConnectivity(member.ID, models.ConnectivityCluster); err != nil || back.AdditionalGateway {
		t.Errorf("back to cluster = %+v, %v; want the additional-gateway mark cleared", back, err)
	}
	if _, err := s.SetConnectivity(gateway.ID, models.ConnectivityCluster); !errors.Is(err, ErrSwarmGatewayNode) {
		t.Errorf("cluster connectivity on the gateway node = %v, want ErrSwarmGatewayNode", err)
	}
	if j, err := s.SetConnectivity(joining.ID, models.ConnectivityEdgeGateway); err != nil || j.AdditionalGateway {
		t.Errorf("a node that has not joined the swarm = %+v, %v; want edge gateway without the additional mark", j, err)
	}
}
