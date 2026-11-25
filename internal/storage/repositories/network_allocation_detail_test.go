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

package repositories

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type detailWorkspace struct {
	ID          uint `gorm:"primaryKey"`
	Name        string
	DisplayName string
}

func (detailWorkspace) TableName() string { return "workspaces" }

type detailNetwork struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	Name        string
	DisplayName string
	DockerName  string
}

func (detailNetwork) TableName() string { return "networks" }

type detailStack struct {
	ID            uint `gorm:"primaryKey"`
	WorkspaceID   uint
	Name          string
	DisplayName   string
	DockerNetwork string
}

func (detailStack) TableName() string { return "stacks" }

type detailServer struct {
	ID          uint `gorm:"primaryKey"`
	Name        string
	DisplayName string
}

func (detailServer) TableName() string { return "servers" }

func TestListDetailedResolvesOwnersAndNodes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.NetworkAllocation{}, &detailWorkspace{}, &detailNetwork{}, &detailStack{}, &detailServer{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&detailWorkspace{ID: 1, Name: "shop", DisplayName: "Shop"})
	db.Create(&detailWorkspace{ID: 2, Name: "blog"})
	db.Create(&detailNetwork{ID: 1, WorkspaceID: 1, Name: "default", DockerName: "mb-ws1-abc"})
	db.Create(&detailStack{ID: 1, WorkspaceID: 2, Name: "wp", DisplayName: "WordPress", DockerNetwork: "mb-stack-ws2-wp"})
	db.Create(&detailServer{ID: 3, Name: "safari-01"})
	for _, a := range []models.NetworkAllocation{
		{DockerName: "mb-ws1-abc", Subnet: "10.64.0.0/24", Gateway: "10.64.0.1", Kind: models.NetAllocKindWorkspace},
		{DockerName: "mb-stack-ws2-wp", Subnet: "10.64.1.0/24", Gateway: "10.64.1.1", Kind: models.NetAllocKindStack, NodeID: 3},
		{DockerName: "miabi-ingress", Subnet: "10.64.2.0/24", Gateway: "10.64.2.1", Kind: models.NetAllocKindGateway},
		{DockerName: "reserved:10.64.3.0/24", Subnet: "10.64.3.0/24", Kind: models.NetAllocKindReserved},
	} {
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}

	rows, err := NewNetworkAllocationRepository(db).ListDetailed()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AllocationDetail{}
	for _, r := range rows {
		got[r.DockerName] = r
	}
	if r := got["mb-ws1-abc"]; r.WorkspaceID != 1 || r.WorkspaceName != "Shop" || r.OwnerName != "default" {
		t.Errorf("workspace network = %+v", r)
	}
	if r := got["mb-stack-ws2-wp"]; r.WorkspaceName != "blog" || r.OwnerName != "WordPress" || r.NodeName != "safari-01" {
		t.Errorf("stack network = %+v", r)
	}
	if r := got["miabi-ingress"]; r.WorkspaceID != 0 || r.OwnerName != "" || r.NodeName != "" {
		t.Errorf("ingress overlay = %+v", r)
	}
	if len(rows) != 4 {
		t.Errorf("rows = %d, want 4", len(rows))
	}
}
