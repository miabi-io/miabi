// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type addressServerFixture struct {
	ID             uint `gorm:"primaryKey"`
	IsLocal        bool
	ClusterID      uint
	Connectivity   string
	PublicIP       string
	PublicHostname string
}

func (addressServerFixture) TableName() string { return "servers" }

type addressClusterFixture struct {
	ID              uint `gorm:"primaryKey"`
	Name            string
	IsDefault       bool
	ManagerServerID uint
	IngressServerID uint
	IngressIP       string
	IngressHostname string
}

func (addressClusterFixture) TableName() string { return "clusters" }

func TestNodeAddressesMoveOntoTheirClusters(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&addressServerFixture{}, &addressClusterFixture{}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&addressClusterFixture{ID: 1, Name: "default", IsDefault: true},
		&addressClusterFixture{ID: 2, Name: "frankfurt", ManagerServerID: 5, IngressServerID: 5},
		&addressClusterFixture{ID: 3, Name: "paris", ManagerServerID: 7, IngressServerID: 7, IngressIP: "192.0.2.9"},
		&addressClusterFixture{ID: 4, Name: "lab", ManagerServerID: 8},
		&addressServerFixture{ID: 1, IsLocal: true, ClusterID: 1, Connectivity: "cluster", PublicHostname: "orbstack"},
		&addressServerFixture{ID: 6, ClusterID: 1, Connectivity: "edge-gateway", PublicIP: "203.0.113.6"},
		&addressServerFixture{ID: 5, ClusterID: 2, Connectivity: "edge-gateway", PublicIP: "203.0.113.5", PublicHostname: "Edge.Example.com."},
		&addressServerFixture{ID: 7, ClusterID: 3, Connectivity: "edge-gateway", PublicIP: "203.0.113.7"},
		&addressServerFixture{ID: 8, ClusterID: 4, Connectivity: "edge-gateway", PublicHostname: "miabi-dev"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := clusterIngressAddressStep(context.Background(), db); err != nil {
		t.Fatalf("step: %v", err)
	}

	var clusters []addressClusterFixture
	if err := db.Table("clusters").Order("id").Find(&clusters).Error; err != nil {
		t.Fatal(err)
	}
	want := map[uint][2]string{
		1: {"", ""},
		2: {"203.0.113.5", "edge.example.com"},
		3: {"192.0.2.9", ""},
		4: {"", ""},
	}
	for _, c := range clusters {
		if got := [2]string{c.IngressIP, c.IngressHostname}; got != want[c.ID] {
			t.Errorf("cluster %s address = %v, want %v", c.Name, got, want[c.ID])
		}
	}

	var connectivity string
	if err := db.Table("servers").Select("connectivity").Where("id = ?", 6).Scan(&connectivity).Error; err != nil {
		t.Fatal(err)
	}
	if connectivity != "cluster" {
		t.Errorf("a default-cluster node kept connectivity %q, want cluster", connectivity)
	}
	for _, col := range []string{"public_ip", "public_hostname"} {
		if db.Migrator().HasColumn("servers", col) {
			t.Errorf("servers.%s was not dropped", col)
		}
	}
	if err := clusterIngressAddressStep(context.Background(), db); err != nil {
		t.Errorf("re-run: %v", err)
	}
}
