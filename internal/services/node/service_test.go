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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Stand-in because the real model's gen_random_uuid() default cannot be parsed by sqlite.
type serverRow struct {
	ID      uint `gorm:"primaryKey"`
	Name    string
	IsLocal bool
	// The insert names these columns, so the stand-in must carry them.
	CPUCores         int
	MemoryBytes      int64
	StorageBytes     int64
	StorageFreeBytes int64
	CPUPercent       float64
	MemUsedBytes     int64
}

func (serverRow) TableName() string { return "servers" }

func newRepoWithNodes(t *testing.T, n int, withControlPlane bool) *repositories.ServerRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&serverRow{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := db.Create(&serverRow{Name: "node"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if withControlPlane {
		if err := db.Create(&serverRow{Name: "manager", IsLocal: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return repositories.NewServerRepository(db)
}

type swarmRow struct {
	ID          uint `gorm:"primaryKey"`
	Name        string
	SwarmNodeID string
	// GORM stamps updated_at on a column-scoped Update, so the table needs it.
	UpdatedAt time.Time
}

func (swarmRow) TableName() string { return "servers" }

func TestNameBySwarmNodeID(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&swarmRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&swarmRow{Name: "worker-a", SwarmNodeID: "abc123"}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(repositories.NewServerRepository(db), nil)

	if got := s.NameBySwarmNodeID("abc123"); got != "worker-a" {
		t.Fatalf("matched swarm id should resolve to name, got %q", got)
	}
	if got := s.NameBySwarmNodeID("nope"); got != "" {
		t.Fatalf("unknown swarm id should resolve to empty, got %q", got)
	}
	if got := s.NameBySwarmNodeID(""); got != "" {
		t.Fatalf("empty swarm id should resolve to empty, got %q", got)
	}
}

func TestCheckNodeLimit(t *testing.T) {
	cases := []struct {
		name         string
		existing     int
		controlPlane bool
		limit        int
		wantBlock    bool
	}{
		{"below limit", 2, false, 3, false},
		{"at limit blocks next", 3, false, 3, true},
		{"over limit blocks next", 4, false, 3, true},
		{"unlimited (-1)", 9, false, -1, false},
		{"control plane not counted", 2, true, 3, false},
		{"control plane plus limit blocks next", 3, true, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewService(newRepoWithNodes(t, tc.existing, tc.controlPlane), nil)
			s.SetNodeLimit(func() int { return tc.limit })
			err := s.checkNodeLimit()
			var limitErr *NodeLimitError
			if tc.wantBlock {
				if !errors.As(err, &limitErr) {
					t.Fatalf("want *NodeLimitError, got %v", err)
				}
				if limitErr.Limit != tc.limit {
					t.Errorf("limit = %d, want %d", limitErr.Limit, tc.limit)
				}
			} else if err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}

// A nil cap closure must never touch the repository, so a nil repo is safe.
func TestCheckNodeLimitUnconfigured(t *testing.T) {
	s := NewService(nil, nil) // no SetNodeLimit, no repo
	if err := s.checkNodeLimit(); err != nil {
		t.Fatalf("unconfigured cap should allow, got %v", err)
	}
}

func TestNodeLimitErrorEnvelope(t *testing.T) {
	e := &NodeLimitError{Limit: 3}
	if e.Code() != "NODE_LIMIT_REACHED" {
		t.Errorf("Code() = %q", e.Code())
	}
	if e.Message() == "" || e.Error() == "" {
		t.Error("Message()/Error() must be populated for the API envelope")
	}
}

// The real model does not AutoMigrate under sqlite (Postgres-flavoured index DDL).
type nameRow struct {
	ID                      uint `gorm:"primaryKey"`
	UID                     string
	Name                    string
	DisplayName             string
	ClusterID               uint
	Connectivity            string
	AccessMode              string
	DockerEndpoint          string
	Address                 string
	IsLocal                 bool
	Role                    string
	Status                  string
	TokenHash               string
	TLSCACert               string
	TLSCert                 string
	TLSKeyEnc               string
	Cordoned                bool
	GatewayTokenEnc         string
	GatewayConfigYAML       string
	GatewayImage            string
	GatewayContainer        string
	GatewayImported         bool
	GatewayRedisPasswordEnc string
	GatewayDeployedAt       *time.Time
	AgentVersion            string
	EngineVersion           string
	SwarmNodeID             string
	AutoJoined              bool
	SwarmRole               string
	SwarmAvailability       string
	SwarmState              string
	InSwarm                 bool
	Labels                  string
	GatewayUpdate           string
	LastSeenAt              *time.Time
	CPUCores                int
	MemoryBytes             int64
	StorageBytes            int64
	StorageFreeBytes        int64
	CapacityMeasuredAt      *time.Time
	CPUPercent              float64
	MemUsedBytes            int64
	UsageMeasuredAt         *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (nameRow) TableName() string { return "servers" }

func nameRepo(t *testing.T) *repositories.ServerRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&nameRow{}); err != nil {
		t.Fatal(err)
	}
	return repositories.NewServerRepository(db)
}

// A second node with the same label gets a distinct handle rather than colliding on the unique index.
func TestCreateNodeDerivesHandleFromDisplayName(t *testing.T) {
	s := NewService(nameRepo(t), nil)

	a, _, err := s.CreateNode(NodeInput{DisplayName: "Frankfurt Edge"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.Name != "frankfurt-edge" {
		t.Errorf("handle = %q, want frankfurt-edge", a.Name)
	}
	if a.DisplayName != "Frankfurt Edge" {
		t.Errorf("label = %q, want the text as typed", a.DisplayName)
	}

	b, _, err := s.CreateNode(NodeInput{DisplayName: "Frankfurt Edge"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if b.Name == a.Name {
		t.Errorf("two nodes share the handle %q; it is the unique key", b.Name)
	}
}

func TestNewNodesDefaultToEdgeGateway(t *testing.T) {
	s := NewService(nameRepo(t), nil)

	srv, _, err := s.CreateNode(NodeInput{DisplayName: "Paris"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if srv.Connectivity != models.ConnectivityEdgeGateway {
		t.Errorf("connectivity = %q, want edge-gateway", srv.Connectivity)
	}
	if _, _, err := s.CreateNode(NodeInput{DisplayName: "Lyon", Connectivity: models.ConnectivityCluster}); !errors.Is(err, ErrInvalidConnectivity) {
		t.Errorf("cluster create err = %v, want ErrInvalidConnectivity", err)
	}
}

// Port-forward is gone: a client still sending it is refused rather than silently ignored.
func TestRetiredConnectivityIsRefused(t *testing.T) {
	s := NewService(nameRepo(t), nil)
	srv, _, err := s.CreateNode(NodeInput{DisplayName: "Paris"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := s.CreateNode(NodeInput{DisplayName: "Lyon", Connectivity: "port-forward"}); !errors.Is(err, ErrInvalidConnectivity) {
		t.Errorf("create err = %v, want ErrInvalidConnectivity", err)
	}
	in := NodeInput{DisplayName: "Paris", Connectivity: "port-forward", Acknowledge: true}
	if _, err := s.UpdateNode(srv.ID, in); !errors.Is(err, ErrInvalidConnectivity) {
		t.Errorf("update err = %v, want ErrInvalidConnectivity", err)
	}
	if _, err := s.SetConnectivity(srv.ID, "port-forward"); !errors.Is(err, ErrInvalidConnectivity) {
		t.Errorf("set err = %v, want ErrInvalidConnectivity", err)
	}
}

// The handle is not settable, so a rename can never move the URL an edge gateway polls.
func TestUpdateNodeRenamesLabelOnly(t *testing.T) {
	s := NewService(nameRepo(t), nil)
	srv, _, err := s.CreateNode(NodeInput{DisplayName: "Frankfurt Edge"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := s.UpdateNode(srv.ID, NodeInput{DisplayName: "Frankfurt Edge 2"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if updated.DisplayName != "Frankfurt Edge 2" {
		t.Errorf("label not updated: %q", updated.DisplayName)
	}
	if updated.Name != srv.Name {
		t.Errorf("handle moved from %q to %q", srv.Name, updated.Name)
	}

	// Repeated renames never touch the handle.
	again, err := s.UpdateNode(srv.ID, NodeInput{DisplayName: "Somewhere Else"})
	if err != nil {
		t.Fatalf("second rename: %v", err)
	}
	if again.Name != srv.Name {
		t.Errorf("handle moved on a later rename: %q", again.Name)
	}
}

// The manager's label is not its handle, so it is editable; its Docker access stays fixed.
func TestUpdateManagerRenamesLabel(t *testing.T) {
	repo := nameRepo(t)
	local, err := repo.EnsureLocal("manager", "unix:///var/run/docker.sock")
	if err != nil {
		t.Fatalf("ensure local: %v", err)
	}
	s := NewService(repo, nil)

	updated, err := s.UpdateNode(local.ID, NodeInput{
		DisplayName:  "Control Plane",
		Connectivity: local.Connectivity,
	})
	if err != nil {
		t.Fatalf("rename manager: %v", err)
	}
	if updated.DisplayName != "Control Plane" {
		t.Errorf("manager label = %q, want Control Plane", updated.DisplayName)
	}
	if updated.Name != local.Name {
		t.Errorf("manager handle moved from %q to %q", local.Name, updated.Name)
	}
	if !updated.IsLocal {
		t.Error("the manager should still be the local node")
	}
}

// A failed mirror still saves the flag but says so: Swarm would otherwise keep placing service tasks.
func TestSetCordonedMirrorsToTheSwarmScheduler(t *testing.T) {
	repo := nameRepo(t)
	srv := &models.Server{Name: "worker-a"}
	if err := repo.Create(srv); err != nil {
		t.Fatal(err)
	}
	s := NewService(repo, nil)

	cordoned := func() bool {
		t.Helper()
		got, err := repo.FindByID(srv.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Cordoned
	}

	// Unwired, the cordon stays Miabi-side and reports no problem.
	if err := s.SetCordoned(context.Background(), srv.ID, true); err != nil {
		t.Fatalf("cordon without a mirror: %v", err)
	}
	if !cordoned() {
		t.Error("the node should be cordoned")
	}

	var got []bool
	s.SetSwarmCordon(func(_ context.Context, serverID uint, c bool) error {
		if serverID != srv.ID {
			t.Errorf("mirrored node %d, want %d", serverID, srv.ID)
		}
		got = append(got, c)
		return nil
	})
	if err := s.SetCordoned(context.Background(), srv.ID, false); err != nil {
		t.Fatalf("uncordon: %v", err)
	}
	if len(got) != 1 || got[0] {
		t.Errorf("mirror calls = %v, want one call with cordoned=false", got)
	}
	if cordoned() {
		t.Error("the node should no longer be cordoned")
	}

	s.SetSwarmCordon(func(context.Context, uint, bool) error { return errors.New("swarm manager unreachable") })
	if err := s.SetCordoned(context.Background(), srv.ID, true); !errors.Is(err, ErrSwarmNotMirrored) {
		t.Errorf("failed mirror err = %v, want ErrSwarmNotMirrored", err)
	}
	if !cordoned() {
		t.Error("the flag should be saved even when the mirror fails")
	}
}
