// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package node

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// serverRow is a minimal stand-in for models.Server so the "servers" table can
// be created under sqlite. The real model defaults its uid column to the
// Postgres-only gen_random_uuid(), which sqlite cannot parse; the cap only reads
// the row count, so id + name is enough here (mirrors the account_test pattern).
type serverRow struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

func (serverRow) TableName() string { return "servers" }

func newRepoWithNodes(t *testing.T, n int) *repositories.ServerRepository {
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
	return repositories.NewServerRepository(db)
}

// swarmRow adds the swarm_node_id column to the minimal servers stand-in so
// NameBySwarmNodeID's swarm-id → name correlation can be exercised under sqlite.
type swarmRow struct {
	ID          uint `gorm:"primaryKey"`
	Name        string
	SwarmNodeID string
	// GORM stamps updated_at on a column-scoped Update (the real model embeds
	// gorm.Model), so the stand-in table needs it or the write fails.
	UpdatedAt time.Time
}

func (swarmRow) TableName() string { return "servers" }

// TestNameBySwarmNodeID checks the swarm-id → display-name correlation used to
// show a cluster app's real replica placement: a matching node resolves to its
// name; an empty or unknown id resolves to "".
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

// TestCheckNodeLimit covers the edition cap decision: blocked at/over the limit
// with a typed, coded error; allowed below it.
func TestCheckNodeLimit(t *testing.T) {
	cases := []struct {
		name      string
		existing  int
		limit     int
		wantBlock bool
	}{
		{"below limit", 2, 3, false},
		{"at limit blocks next", 3, 3, true},
		{"over limit blocks next", 4, 3, true},
		{"unlimited (-1)", 9, -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewService(newRepoWithNodes(t, tc.existing), nil)
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

// TestCheckNodeLimitUnconfigured verifies a nil cap closure imposes no limit and
// never touches the repository (so a nil repo is safe).
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

// nameRow is the servers stand-in for the naming contract. The real model does
// not AutoMigrate under sqlite (its index DDL is Postgres-flavoured), which is
// why the tests in this file use stand-ins; this one carries every column the
// create/update path writes.
type nameRow struct {
	ID                      uint `gorm:"primaryKey"`
	UID                     string
	Name                    string
	DisplayName             string
	Connectivity            string
	AccessMode              string
	DockerEndpoint          string
	Address                 string
	PublicIP                string
	PublicHostname          string
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
	SwarmNodeID             string
	AutoJoined              bool
	SwarmRole               string
	SwarmAvailability       string
	SwarmState              string
	InSwarm                 bool
	Labels                  string
	GatewayUpdate           string
	LastSeenAt              *time.Time
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

// The handle is derived from the label unless one is supplied, and a second node
// with the same label gets a distinct handle rather than colliding on the unique
// index.
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

// The label is editable; the handle is not settable at all, so a rename can never
// move the URL an edge gateway polls.
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

// The manager's label is editable like any other node's — it is not the handle,
// so nothing resolves through it. Its Docker access stays fixed.
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
