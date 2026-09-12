// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package placement

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type clusterTable struct {
	ID              uint `gorm:"primaryKey"`
	UID             string
	Name            string
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
}

func (clusterTable) TableName() string { return "clusters" }

type serverTable struct {
	ID        uint `gorm:"primaryKey"`
	Name      string
	ClusterID uint
	IsLocal   bool
	Cordoned  bool
}

func (serverTable) TableName() string { return "servers" }

type workspaceTable struct {
	ID               uint `gorm:"primaryKey"`
	DefaultClusterID *uint
	UpdatedAt        time.Time
}

func (workspaceTable) TableName() string { return "workspaces" }

type placedTable struct {
	ID          uint `gorm:"primaryKey"`
	ServerID    uint
	ClusterID   uint
	MemoryBytes int64
	RuntimeKind string
}

func newPlacement(t *testing.T, online map[uint]bool) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&clusterTable{}, &serverTable{}, &workspaceTable{}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"applications", "database_instances", "volumes"} {
		if err := db.Table(table).AutoMigrate(&placedTable{}); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(v any) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed(&[]clusterTable{
		{ID: 1, Name: "default", DisplayName: "Paris", Mode: "standalone", IsDefault: true, Visibility: "all"},
		{ID: 2, Name: "eu-east", DisplayName: "Warsaw", LocationCode: "eu-east", Mode: "swarm", ManagerServerID: 10, Visibility: "all"},
		{ID: 3, Name: "gpu", Mode: "standalone", ManagerServerID: 20, Visibility: "restricted"},
		{ID: 4, Name: "closing", Mode: "standalone", ManagerServerID: 30, Visibility: "all", Cordoned: true},
	})
	seed(&[]serverTable{
		{ID: 1, Name: "manager", ClusterID: 1, IsLocal: true},
		{ID: 10, Name: "warsaw-1", ClusterID: 2},
		{ID: 11, Name: "warsaw-2", ClusterID: 2},
		{ID: 12, Name: "warsaw-3", ClusterID: 2},
		{ID: 13, Name: "warsaw-4", ClusterID: 2, Cordoned: true},
		{ID: 20, Name: "gpu-1", ClusterID: 3},
	})
	seed(&workspaceTable{ID: 5})
	if err := db.Table("applications").Create(&[]placedTable{
		{ID: 1, ServerID: 10, ClusterID: 2, MemoryBytes: 2 << 30},
		{ID: 2, ServerID: 11, ClusterID: 2, MemoryBytes: 1 << 30},
		{ID: 3, ServerID: 11, ClusterID: 2, MemoryBytes: 8 << 30, RuntimeKind: "service"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	isOnline := func(id uint) bool { return online[id] }
	return NewService(repositories.NewClusterRepository(db), repositories.NewServerRepository(db), isOnline), db
}

func TestPlacementPicksTheLeastLoadedOnlineNode(t *testing.T) {
	s, _ := newPlacement(t, map[uint]bool{10: true, 11: true, 13: true})

	got, err := s.Place(Request{WorkspaceID: 5, Location: "eu-east"})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	// 12 is offline and 13 cordoned; 11 carries less container memory than 10 (its service app does not count).
	if got.ClusterID != 2 || got.ServerID != 11 {
		t.Errorf("placed on cluster %d node %d, want cluster 2 node 11", got.ClusterID, got.ServerID)
	}

	svc, err := s.Place(Request{WorkspaceID: 5, Location: "eu-east", Service: true})
	if err != nil || svc.ServerID != 10 {
		t.Errorf("service app placed on node %d (%v), want the cluster's manager node 10", svc.ServerID, err)
	}
}

func TestPlacementFallsBackToTheWorkspaceDefaultThenTheDefaultCluster(t *testing.T) {
	s, _ := newPlacement(t, map[uint]bool{10: true})

	got, err := s.Place(Request{WorkspaceID: 5})
	if err != nil || got.ClusterID != 1 || got.ServerID != 1 {
		t.Fatalf("no default: placed %+v (%v), want the default cluster's local node", got, err)
	}

	if err := s.SetDefaultLocation(5, "eu-east", false); err != nil {
		t.Fatalf("set default: %v", err)
	}
	got, err = s.Place(Request{WorkspaceID: 5})
	if err != nil || got.ClusterID != 2 {
		t.Fatalf("with a default: placed %+v (%v), want eu-east", got, err)
	}

	locs, err := s.Locations(5, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range locs {
		names = append(names, l.Name)
		if l.Default != (l.Name == "eu-east") {
			t.Errorf("location %s default = %v", l.Name, l.Default)
		}
	}
	if len(names) != 2 {
		t.Errorf("locations = %v, want default and eu-east (gpu is restricted, closing is cordoned)", names)
	}
}

func TestPlacementRefusals(t *testing.T) {
	s, _ := newPlacement(t, map[uint]bool{})

	cases := []struct {
		name string
		req  Request
		want error
	}{
		{"unknown location", Request{WorkspaceID: 5, Location: "mars"}, ErrLocationNotFound},
		{"restricted location", Request{WorkspaceID: 5, Location: "gpu"}, ErrLocationNotAllowed},
		{"cordoned location", Request{WorkspaceID: 5, Location: "closing"}, ErrLocationCordoned},
		{"no reachable node", Request{WorkspaceID: 5, Location: "eu-east"}, ErrNoSchedulableNode},
		{"node pin by a tenant", Request{WorkspaceID: 5, ServerID: 10}, ErrNodePinAdminOnly},
		{"node outside the location", Request{WorkspaceID: 5, ServerID: 20, Location: "eu-east", Admin: true}, ErrLocationMismatch},
	}
	for _, tc := range cases {
		if _, err := s.Place(tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}

	if got, err := s.Place(Request{WorkspaceID: 5, Location: "gpu", Admin: true}); err != nil || got.ServerID != 20 {
		t.Errorf("admin in a restricted location: %+v (%v), want node 20", got, err)
	}
	if got, err := s.Place(Request{WorkspaceID: 5, Colocate: 20}); err != nil || got.ClusterID != 3 {
		t.Errorf("colocation: %+v (%v), want the database's node and cluster", got, err)
	}
	if err := s.SetDefaultLocation(5, "gpu", false); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("tenant default in a restricted location err = %v", err)
	}
}

var _ = models.DefaultClusterID
