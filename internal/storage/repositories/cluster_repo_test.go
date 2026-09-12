// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// clusterTable mirrors models.Cluster without the Postgres-only uid default sqlite cannot parse.
type clusterTable struct {
	ID              uint `gorm:"primaryKey"`
	UID             string
	Name            string `gorm:"uniqueIndex"`
	DisplayName     string
	LocationCode    string
	Mode            string
	IsDefault       bool
	ManagerServerID uint
	AgentTokenHash  string
	IngressServerID uint
	IngressIP       string
	IngressHostname string
	Visibility      string
	Cordoned        bool
	LegacyIngress   bool
	CreatedAt       time.Time
	UpdatedAt       time.Time

	ExternalBaseDomain   string
	ExternalCertProvider string
	ServiceEndpointMode  string
}

func (clusterTable) TableName() string { return "clusters" }

type clusterRouteTable struct {
	ID            uint `gorm:"primaryKey"`
	ApplicationID uint
	Generated     bool
}

func (clusterRouteTable) TableName() string { return "routes" }

type clusterServerTable struct {
	ID        uint `gorm:"primaryKey"`
	Name      string
	ClusterID uint
	UpdatedAt time.Time
}

func (clusterServerTable) TableName() string { return "servers" }

type clusterPlacedTable struct {
	ID        uint `gorm:"primaryKey"`
	ServerID  uint
	ClusterID uint
	UpdatedAt time.Time
}

func newClusterRepo(t *testing.T) (*ClusterRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&clusterTable{}, &clusterServerTable{}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"applications", "database_instances", "volumes", "jobs", "stacks"} {
		if err := db.Table(table).AutoMigrate(&clusterPlacedTable{}); err != nil {
			t.Fatal(err)
		}
	}
	return NewClusterRepository(db), db
}

// The count drives the confirmation before a domain change, so an app is counted once however many URLs it has.
func TestCountExternalAppsCountsAppsWithGeneratedRoutes(t *testing.T) {
	repo, db := newClusterRepo(t)
	if err := db.AutoMigrate(&clusterRouteTable{}); err != nil {
		t.Fatal(err)
	}
	for _, app := range []clusterPlacedTable{{ID: 1, ClusterID: 3}, {ID: 2, ClusterID: 3}, {ID: 3, ClusterID: 4}} {
		if err := db.Table("applications").Create(&app).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, rt := range []clusterRouteTable{
		{ID: 1, ApplicationID: 1, Generated: true},
		{ID: 2, ApplicationID: 1, Generated: true},
		{ID: 3, ApplicationID: 2},
		{ID: 4, ApplicationID: 3, Generated: true},
	} {
		if err := db.Create(&rt).Error; err != nil {
			t.Fatal(err)
		}
	}
	if n, err := repo.CountExternalApps(3); err != nil || n != 1 {
		t.Errorf("cluster 3 = %d, %v; want 1 (one app with two generated URLs, one with only a custom route)", n, err)
	}
}

func TestJoiningTheDefaultClusterMovesANodeAndItsWorkloads(t *testing.T) {
	repo, db := newClusterRepo(t)
	if err := db.Create(&clusterTable{ID: 1, Name: "default", IsDefault: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&clusterServerTable{ID: 7, Name: "paris"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("applications").Create(&clusterPlacedTable{ID: 1, ServerID: 7}).Error; err != nil {
		t.Fatal(err)
	}

	srv := &models.Server{ID: 7, Name: "paris", Connectivity: models.ConnectivityEdgeGateway}
	standalone, err := repo.CreateStandalone(srv, "paris")
	if err != nil {
		t.Fatalf("create standalone: %v", err)
	}
	if srv.ClusterID != standalone.ID || standalone.IngressServerID != 7 || standalone.ManagerServerID != 7 {
		t.Fatalf("standalone = %+v, server cluster = %d; want an edge cluster run through node 7", standalone, srv.ClusterID)
	}
	if got := clusterOf(t, db, "applications", 1); got != standalone.ID {
		t.Fatalf("app cluster = %d, want the node's standalone cluster %d", got, standalone.ID)
	}
	if err := db.Table("stacks").Create(&clusterPlacedTable{ID: 1, ClusterID: standalone.ID}).Error; err != nil {
		t.Fatal(err)
	}

	if err := repo.AssignServer(7, 1); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if got := clusterOf(t, db, "applications", 1); got != 1 {
		t.Errorf("app cluster = %d after joining, want the default cluster", got)
	}
	if got := clusterOf(t, db, "stacks", 1); got != 1 {
		t.Errorf("stack cluster = %d after its only node left, want the default cluster", got)
	}
	if _, err := repo.FindByID(standalone.ID); err == nil {
		t.Error("the emptied standalone cluster was kept")
	}

	list, err := repo.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].NodeCount != 1 {
		t.Errorf("clusters = %+v, want only the default cluster holding one node", list)
	}
}

func TestTheDefaultClusterIsNeverDeletedWhenEmpty(t *testing.T) {
	repo, db := newClusterRepo(t)
	if err := db.Create(&clusterTable{ID: 1, Name: "default", IsDefault: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteIfEmpty(1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindDefault(); err != nil {
		t.Errorf("default cluster gone: %v", err)
	}
}

func clusterOf(t *testing.T, db *gorm.DB, table string, id uint) uint {
	t.Helper()
	var row clusterPlacedTable
	if err := db.Table(table).First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	return row.ClusterID
}
