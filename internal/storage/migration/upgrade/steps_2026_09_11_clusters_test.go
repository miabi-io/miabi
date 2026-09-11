// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type serverFixture struct {
	ID           uint `gorm:"primaryKey"`
	Name         string
	DisplayName  string
	IsLocal      bool
	Connectivity string
	SwarmNodeID  string
	ClusterID    uint
}

func (serverFixture) TableName() string { return "servers" }

type placedFixture struct {
	ID        uint `gorm:"primaryKey"`
	ServerID  uint
	StackID   *uint
	ClusterID uint
}

type stackFixture struct {
	ID        uint `gorm:"primaryKey"`
	Name      string
	ClusterID uint
}

func (stackFixture) TableName() string { return "stacks" }

type clusterSettingFixture struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

func (clusterSettingFixture) TableName() string { return "settings" }

func newClustersDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&clusterRow{}, &serverFixture{}, &stackFixture{}, &clusterSettingFixture{}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"applications", "database_instances", "volumes", "jobs"} {
		if err := db.Table(table).AutoMigrate(&placedFixture{}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func u(v uint) *uint { return &v }

func TestClustersStepMapsAnExistingInstall(t *testing.T) {
	db := newClustersDB(t)
	seed := func(v any) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed(&[]serverFixture{
		{ID: 1, Name: "manager", IsLocal: true, Connectivity: "edge-gateway", SwarmNodeID: "mgr"},
		{ID: 2, Name: "worker-a", Connectivity: "port-forward", SwarmNodeID: "wa"},
		{ID: 3, Name: "frankfurt", DisplayName: "Frankfurt", Connectivity: "edge-gateway"},
		{ID: 4, Name: "lab", Connectivity: "port-forward"},
	})
	seed(&[]stackFixture{{ID: 1, Name: "same"}, {ID: 2, Name: "edge"}, {ID: 3, Name: "mixed"}})
	seed(&[]clusterSettingFixture{{Key: "cluster_name", Value: " prod-eu "}, {Key: "cluster_agent_token_hash", Value: "abc"}})
	apps := []placedFixture{
		{ID: 1, ServerID: 0, StackID: u(1)},
		{ID: 2, ServerID: 2, StackID: u(1)},
		{ID: 3, ServerID: 3, StackID: u(2)},
		{ID: 4, ServerID: 3, StackID: u(3)},
		{ID: 5, ServerID: 4, StackID: u(3)},
	}
	if err := db.Table("applications").Create(&apps).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("database_instances").Create(&placedFixture{ID: 1, ServerID: 4}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("volumes").Create(&placedFixture{ID: 1, ServerID: 3}).Error; err != nil {
		t.Fatal(err)
	}

	for run := 1; run <= 2; run++ {
		if err := clustersStep(context.Background(), db); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}

	var clusters []clusterRow
	if err := db.Order("id").Find(&clusters).Error; err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 3 {
		t.Fatalf("clusters = %d, want default + frankfurt + lab (re-running must not add any)", len(clusters))
	}
	def, frankfurt, lab := clusters[0], clusters[1], clusters[2]
	if !def.IsDefault || def.Mode != "swarm" || def.DisplayName != "prod-eu" || def.AgentTokenHash != "abc" {
		t.Errorf("default = %+v, want a swarm named prod-eu holding the agent token hash", def)
	}
	if frankfurt.DisplayName != "Frankfurt" || frankfurt.IngressServerID != 3 || frankfurt.LegacyIngress {
		t.Errorf("frankfurt = %+v, want an edge standalone cluster served by node 3", frankfurt)
	}
	if lab.ManagerServerID != 4 || lab.IngressServerID != 0 || !lab.LegacyIngress {
		t.Errorf("lab = %+v, want a legacy-ingress standalone cluster managed through node 4", lab)
	}

	wantServers := map[uint]uint{1: def.ID, 2: def.ID, 3: frankfurt.ID, 4: lab.ID}
	var servers []serverFixture
	if err := db.Find(&servers).Error; err != nil {
		t.Fatal(err)
	}
	for _, s := range servers {
		if s.ClusterID != wantServers[s.ID] {
			t.Errorf("server %s cluster = %d, want %d", s.Name, s.ClusterID, wantServers[s.ID])
		}
	}

	wantApps := map[uint]uint{1: def.ID, 2: def.ID, 3: frankfurt.ID, 4: frankfurt.ID, 5: lab.ID}
	var gotApps []placedFixture
	if err := db.Table("applications").Find(&gotApps).Error; err != nil {
		t.Fatal(err)
	}
	for _, a := range gotApps {
		if a.ClusterID != wantApps[a.ID] {
			t.Errorf("app %d cluster = %d, want %d", a.ID, a.ClusterID, wantApps[a.ID])
		}
	}
	assertCluster(t, db, "database_instances", lab.ID)
	assertCluster(t, db, "volumes", frankfurt.ID)

	wantStacks := map[string]uint{"same": def.ID, "edge": frankfurt.ID, "mixed": def.ID}
	var stacks []stackFixture
	if err := db.Find(&stacks).Error; err != nil {
		t.Fatal(err)
	}
	for _, st := range stacks {
		if st.ClusterID != wantStacks[st.Name] {
			t.Errorf("stack %s cluster = %d, want %d", st.Name, st.ClusterID, wantStacks[st.Name])
		}
	}

	var leftover int64
	if err := db.Model(&clusterSettingFixture{}).Count(&leftover).Error; err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Errorf("%d cluster settings left behind after being copied", leftover)
	}
}

func TestClustersStepOnAFreshInstall(t *testing.T) {
	db := newClustersDB(t)
	if err := clustersStep(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var def clusterRow
	if err := db.Where("is_default = ?", true).First(&def).Error; err != nil {
		t.Fatalf("no default cluster on an empty install: %v", err)
	}
	if def.Name != "default" || def.Mode != "standalone" {
		t.Errorf("default = %+v, want a standalone cluster named default", def)
	}
}

func assertCluster(t *testing.T, db *gorm.DB, table string, want uint) {
	t.Helper()
	var row placedFixture
	if err := db.Table(table).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ClusterID != want {
		t.Errorf("%s cluster = %d, want %d", table, row.ClusterID, want)
	}
}
