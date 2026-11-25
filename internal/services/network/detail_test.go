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

package network

import (
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/netalloc"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type detailApp struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	Name        string
	DisplayName string
	ServerID    uint
}

func (detailApp) TableName() string { return "applications" }

type detailDB struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	Name        string
	DisplayName string
	ServerID    uint
}

func (detailDB) TableName() string { return "database_instances" }

type appNet struct{ ApplicationID, NetworkID uint }

func (appNet) TableName() string { return "application_networks" }

type dbNet struct{ DatabaseInstanceID, NetworkID uint }

func (dbNet) TableName() string { return "database_instance_networks" }

// The view used to read only the control plane, so a network used on remote nodes looked missing.
func TestDetailReportsEveryNodeTheNetworksResourcesRunOn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Network{}, &detailApp{}, &detailDB{}, &appNet{}, &dbNet{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&models.Network{ID: 7, WorkspaceID: 3, Name: "backend", DockerName: "mb-ws3-abc", Driver: DriverBridge})
	db.Create(&detailApp{ID: 1, WorkspaceID: 3, Name: "api", DisplayName: "API", ServerID: 2})
	db.Create(&detailApp{ID: 2, WorkspaceID: 3, Name: "worker", ServerID: 4})
	db.Create(&detailDB{ID: 9, WorkspaceID: 3, Name: "pg", ServerID: 5})
	db.Create(&appNet{ApplicationID: 1, NetworkID: 7})
	db.Create(&appNet{ApplicationID: 2, NetworkID: 7})
	db.Create(&dbNet{DatabaseInstanceID: 9, NetworkID: 7})

	copyAt := func(subnet string) *nodeEngine {
		return &nodeEngine{nets: []docker.Network{{Name: "mb-ws3-abc", Subnet: subnet, Gateway: "gw", Scope: "local"}}}
	}
	s := &Service{
		repo:    repositories.NewNetworkRepository(db),
		docker:  &nodeEngine{},
		clients: nodeClients{2: copyAt("10.64.5.0/24"), 4: copyAt("172.30.0.0/16")},
		servers: nodeList{{ID: 1, Name: "manager", IsLocal: true}, {ID: 2, Name: "edge-a"}, {ID: 4, Name: "edge-b"}, {ID: 5, Name: "edge-c"}, {ID: 6, Name: "unrelated"}},
	}

	d, err := s.Detail(context.Background(), 3, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Apps) != 2 || d.Apps[0].Label != "API" || len(d.Databases) != 1 || d.Databases[0].Label != "pg" {
		t.Errorf("attached = apps %+v, databases %+v", d.Apps, d.Databases)
	}
	got := map[string]NodePresence{}
	for _, p := range d.Nodes {
		got[p.NodeName] = p
	}
	if len(d.Nodes) != 4 {
		t.Errorf("nodes = %+v, want the control plane and the three nodes in use, not the unrelated one", d.Nodes)
	}
	if p := got["manager"]; !p.Reachable || p.Exists {
		t.Errorf("control plane = %+v, want reachable without a copy", p)
	}
	if p := got["edge-a"]; !p.Exists || p.Subnet != "10.64.5.0/24" {
		t.Errorf("edge-a = %+v", p)
	}
	if p := got["edge-c"]; p.Reachable {
		t.Errorf("offline edge-c = %+v, want unreachable", p)
	}
	if !d.Exists || d.PoolManaged || d.Subnet == "" {
		t.Errorf("detail = exists %v, pool %v, subnet %q; want a live copy's addressing without a pool", d.Exists, d.PoolManaged, d.Subnet)
	}
}

func TestDetailUsesThePoolSubnetAndFlagsDriftedCopies(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Network{}, &models.NetworkAllocation{}, &detailApp{}, &detailDB{}, &appNet{}, &dbNet{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&models.Network{ID: 7, WorkspaceID: 3, Name: "backend", DockerName: "mb-ws3-abc", Driver: DriverBridge})
	db.Create(&detailApp{ID: 1, WorkspaceID: 3, Name: "api", ServerID: 2})
	db.Create(&detailApp{ID: 2, WorkspaceID: 3, Name: "worker", ServerID: 4})
	db.Create(&appNet{ApplicationID: 1, NetworkID: 7})
	db.Create(&appNet{ApplicationID: 2, NetworkID: 7})
	pool, err := netalloc.NewService(repositories.NewNetworkAllocationRepository(db), "10.64.0.0/16", 24)
	if err != nil {
		t.Fatal(err)
	}
	a, err := pool.Allocate("mb-ws3-abc", 0, models.NetAllocKindWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	copyAt := func(subnet string) *nodeEngine {
		return &nodeEngine{nets: []docker.Network{{Name: "mb-ws3-abc", Subnet: subnet, Scope: "local"}}}
	}
	s := &Service{
		repo: repositories.NewNetworkRepository(db), alloc: pool, docker: copyAt(a.Subnet),
		clients: nodeClients{2: copyAt(a.Subnet), 4: copyAt("172.30.0.0/16")},
		servers: nodeList{{ID: 1, Name: "manager", IsLocal: true}, {ID: 2, Name: "edge-a"}, {ID: 4, Name: "edge-b"}},
	}
	d, err := s.Detail(context.Background(), 3, 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Databases == nil {
		t.Error("databases is nil for a network without any, which reaches the UI as null")
	}
	if !d.PoolManaged || d.Subnet != a.Subnet || d.Gateway != a.Gateway {
		t.Errorf("detail addressing = %q/%q pool %v, want the pool's %s", d.Subnet, d.Gateway, d.PoolManaged, a.Subnet)
	}
	for _, p := range d.Nodes {
		if want := p.NodeName == "edge-b"; p.Drifted != want {
			t.Errorf("%s drifted = %v, want %v", p.NodeName, p.Drifted, want)
		}
	}
}
