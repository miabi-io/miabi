// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	ID        uint `gorm:"primaryKey"`
	UID       string
	Name      string
	Mode      string
	IsDefault bool
}

func (clusterRow) TableName() string { return "clusters" }

// Regression: importing a gateway switched a node to edge-gateway without the check UpdateNode and
// SetConnectivity apply, so a default-cluster node could be given a gateway of its own.
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
