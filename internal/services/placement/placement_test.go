// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package placement

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type clusterTable struct {
	ID              uint `gorm:"primaryKey"`
	OrganizationID  *uint
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
	ID          uint `gorm:"primaryKey"`
	Name        string
	DisplayName string
	SwarmNodeID string
	ClusterID   uint
	IsLocal     bool
	Cordoned    bool
	Labels      map[string]string `gorm:"serializer:json"`
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
		{ID: 12, Name: "warsaw-3", ClusterID: 2, Labels: map[string]string{models.PoolLabel: "pro"}},
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
		if l.Swarm != (l.Name == "eu-east") {
			t.Errorf("location %s swarm = %v", l.Name, l.Swarm)
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
		{"restricted location reads as unknown", Request{WorkspaceID: 5, Location: "gpu"}, ErrLocationNotFound},
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
	if err := s.SetDefaultLocation(5, "gpu", false); !errors.Is(err, ErrLocationNotFound) {
		t.Errorf("tenant default in a restricted location err = %v", err)
	}
}

type fakePolicy struct{ placement models.PlanPlacement }

func (f fakePolicy) EffectivePlacement(uint) (models.PlanPlacement, bool) { return f.placement, true }

// A pooled plan lands only on its pool's nodes, and a plan without a pool never on a pooled one.
func TestPlacementHonorsThePlanPool(t *testing.T) {
	s, _ := newPlacement(t, map[uint]bool{10: true, 11: true, 12: true})

	s.SetPolicy(fakePolicy{models.PlanPlacement{Pool: "pro"}})
	if got, err := s.Place(Request{WorkspaceID: 5, Location: "eu-east"}); err != nil || got.ServerID != 12 {
		t.Errorf("pro plan placed on node %d (%v), want the pro node 12", got.ServerID, err)
	}
	if got := s.PoolConstraints(5, 2); len(got) != 1 || got[0] != "node.labels.miabi.pool==pro" {
		t.Errorf("pro plan constraints = %v", got)
	}

	s.SetPolicy(fakePolicy{})
	if got, err := s.Place(Request{WorkspaceID: 5, Location: "eu-east"}); err != nil || got.ServerID != 11 {
		t.Errorf("unpooled plan placed on node %d (%v), want the least loaded unpooled node 11", got.ServerID, err)
	}
	if got := s.PoolConstraints(5, 2); len(got) != 1 || got[0] != "node.labels.miabi.pool!=pro" {
		t.Errorf("unpooled plan constraints = %v, want the pro pool excluded", got)
	}
	if got := s.PoolConstraints(5, 1); len(got) != 0 {
		t.Errorf("a cluster without pools needs no constraints, got %v", got)
	}

	s.SetPolicy(fakePolicy{models.PlanPlacement{Pool: "gpu"}})
	for _, loc := range []string{"eu-east", "default"} {
		_, err := s.Place(Request{WorkspaceID: 5, Location: loc})
		if !errors.Is(err, ErrNoSchedulableNode) || !strings.Contains(err.Error(), `"gpu"`) {
			t.Errorf("%s with no gpu node: err = %v, want a refusal naming the pool", loc, err)
		}
	}
}

// A plan's locations bound what a workspace sees and where it may create; the first is its default.
func TestPlacementHonorsThePlanLocations(t *testing.T) {
	s, _ := newPlacement(t, map[uint]bool{10: true, 11: true})
	s.SetPolicy(fakePolicy{models.PlanPlacement{Locations: []uint{2}}})

	if got, err := s.Place(Request{WorkspaceID: 5}); err != nil || got.ClusterID != 2 {
		t.Errorf("placed %+v (%v), want the plan's first location", got, err)
	}
	if _, err := s.Place(Request{WorkspaceID: 5, Location: "default"}); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("a location outside the plan: err = %v", err)
	}
	if err := s.SetDefaultLocation(5, "default", false); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("a default outside the plan: err = %v", err)
	}
	locs, err := s.Locations(5, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 || locs[0].Name != "eu-east" || !locs[0].Default {
		t.Errorf("locations = %+v, want only eu-east, as the default", locs)
	}
}

// A cordoned node is refused wherever it would host the workload — the standalone default cluster
// (the control plane's own node) and a standalone cluster's single node alike. A swarm cluster's
// manager is exempt: it is only the engine a service create is issued through.
func TestPlacementRefusesACordonedHostNode(t *testing.T) {
	s, db := newPlacement(t, map[uint]bool{10: true, 11: true})

	if err := db.Model(&serverTable{}).Where("id IN ?", []uint{1, 20}).
		Update("cordoned", true).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := s.Place(Request{WorkspaceID: 5}); !errors.Is(err, ErrNoSchedulableNode) {
		t.Errorf("default cluster with its node cordoned: err = %v, want ErrNoSchedulableNode", err)
	}
	if _, err := s.Place(Request{WorkspaceID: 5, Location: "gpu", Admin: true}); !errors.Is(err, ErrNoSchedulableNode) {
		t.Errorf("standalone cluster with its node cordoned: err = %v, want ErrNoSchedulableNode", err)
	}

	if err := db.Model(&serverTable{}).Where("id = ?", 10).Update("cordoned", true).Error; err != nil {
		t.Fatal(err)
	}
	got, err := s.Place(Request{WorkspaceID: 5, Location: "eu-east", Service: true})
	if err != nil || got.ServerID != 10 {
		t.Errorf("service on a swarm cluster whose manager is cordoned: node %d (%v), want node 10", got.ServerID, err)
	}
}

// fakeOrgs answers the placement engine's organization questions from a fixed map.
type fakeOrgs struct {
	ofWorkspace map[uint]uint
	owners      map[uint]bool
}

func (f fakeOrgs) OrganizationOfWorkspace(id uint) uint { return f.ofWorkspace[id] }
func (f fakeOrgs) OwnsClusters(orgID uint) bool         { return f.owners[orgID] }
func (f fakeOrgs) OrganizationLabel(orgID uint) string  { return fmt.Sprintf("org-%d", orgID) }

// A cluster dedicated to an organization is invisible to every other tenant, and an organization that
// owns one is CONFINED to it — a dedicated tenant must never quietly land on shared hardware.
func TestPlacementIsolatesOrganizationClusters(t *testing.T) {
	s, db := newPlacement(t, map[uint]bool{10: true, 11: true, 20: true})
	acme := uint(7)
	if err := db.Model(&clusterTable{}).Where("id = ?", 3).
		Updates(map[string]any{"organization_id": acme, "visibility": "organization"}).Error; err != nil {
		t.Fatal(err)
	}
	// Workspace 5 is in Acme, which owns cluster 3; workspace 6 is an ordinary tenant.
	if err := db.Create(&workspaceTable{ID: 6}).Error; err != nil {
		t.Fatal(err)
	}
	s.SetOrgs(fakeOrgs{
		ofWorkspace: map[uint]uint{5: acme, 6: 2},
		owners:      map[uint]bool{acme: true},
	})

	// The owning tenant may place there, and only there.
	if got, err := s.Place(Request{WorkspaceID: 5, Location: "gpu"}); err != nil || got.ClusterID != 3 {
		t.Errorf("acme in its own cluster: %+v (%v), want cluster 3", got, err)
	}
	if _, err := s.Place(Request{WorkspaceID: 5, Location: "default"}); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("acme on a shared cluster: err = %v, want ErrLocationNotAllowed", err)
	}
	if got, err := s.Place(Request{WorkspaceID: 5}); err != nil || got.ClusterID != 3 {
		t.Errorf("acme with no location named: %+v (%v), want its own cluster 3, never a shared fallback", got, err)
	}

	// Another tenant cannot reach it, by name or through the location list. Naming it reads as "no
	// such location" rather than "forbidden", so a stranger cannot confirm the name exists.
	if _, err := s.Place(Request{WorkspaceID: 6, Location: "gpu"}); !errors.Is(err, ErrLocationNotFound) {
		t.Errorf("outsider naming a dedicated cluster: err = %v, want ErrLocationNotFound", err)
	}
	locs, err := s.Locations(6, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range locs {
		if l.Name == "gpu" {
			t.Error("a dedicated cluster must not appear in another tenant's locations")
		}
	}
	if err := s.SetDefaultLocation(6, "gpu", false); !errors.Is(err, ErrLocationNotFound) {
		t.Errorf("outsider defaulting to a dedicated cluster: err = %v", err)
	}

	// Colocation follows an existing resource, so it is the one path that could cross realms.
	if _, err := s.Place(Request{WorkspaceID: 6, Colocate: 20}); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("colocation into another organization's cluster: err = %v, want ErrLocationNotAllowed", err)
	}

	// Confinement binds the WORKSPACE, not the caller: a platform admin deploying into workspace 6
	// still cannot put it in Acme's cluster, and still cannot take workspace 5 off Acme's.
	// An admin already lists every cluster, so hiding it from them buys nothing: they get the
	// accurate refusal instead.
	if _, err := s.Place(Request{WorkspaceID: 6, Location: "gpu", Admin: true}); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("admin placing an outsider in a dedicated cluster: err = %v, want ErrLocationNotAllowed", err)
	}
	if _, err := s.Place(Request{WorkspaceID: 5, Location: "default", Admin: true}); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("admin placing a confined workspace on shared hardware: err = %v, want ErrLocationNotAllowed", err)
	}
	if got, err := s.Place(Request{WorkspaceID: 5, Location: "gpu", Admin: true}); err != nil || got.ClusterID != 3 {
		t.Errorf("admin inside the owning organization: %+v (%v), want cluster 3", got, err)
	}
	// A node pin is the admin's own privilege, but it cannot cross a realm either.
	if _, err := s.Place(Request{WorkspaceID: 6, ServerID: 20, Admin: true}); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("admin pinning a node in another organization's cluster: err = %v, want ErrLocationNotAllowed", err)
	}
	// The admin's own locations view is unchanged for an unconfined workspace: restricted clusters
	// stay visible to them.
	locs6, err := s.Locations(6, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range locs6 {
		if l.Name == "gpu" {
			t.Error("a dedicated cluster must not appear for an admin acting in another realm")
		}
	}
}

// A confined tenant whose own locations are all cordoned gets an error naming the organization:
// "no location is available" would send the operator to the workspace's plan instead of to the
// organization's clusters, which is where the problem is.
func TestConfinedDeadEndNamesTheOrganization(t *testing.T) {
	s, db := newPlacement(t, map[uint]bool{})
	acme := uint(7)
	if err := db.Model(&clusterTable{}).Where("id = ?", 3).
		Updates(map[string]any{"organization_id": acme, "visibility": "organization", "cordoned": true}).Error; err != nil {
		t.Fatal(err)
	}
	s.SetOrgs(fakeOrgs{ofWorkspace: map[uint]uint{5: acme}, owners: map[uint]bool{acme: true}})

	_, err := s.Place(Request{WorkspaceID: 5})
	if !errors.Is(err, ErrNoLocation) {
		t.Fatalf("err = %v, want ErrNoLocation", err)
	}
	if !strings.Contains(err.Error(), "org-7") {
		t.Errorf("err = %q, want it to name the organization", err)
	}
}

// Without dedicated clusters nothing changes: every tenant keeps the shared locations it had.
func TestPlacementUnaffectedWithoutOrganizationClusters(t *testing.T) {
	s, _ := newPlacement(t, map[uint]bool{10: true, 11: true})
	s.SetOrgs(fakeOrgs{ofWorkspace: map[uint]uint{5: 7}})

	if got, err := s.Place(Request{WorkspaceID: 5}); err != nil || got.ClusterID != 1 {
		t.Errorf("placed %+v (%v), want the shared default cluster 1", got, err)
	}
	if got, err := s.Place(Request{WorkspaceID: 5, Location: "eu-east"}); err != nil || got.ClusterID != 2 {
		t.Errorf("placed %+v (%v), want the shared cluster 2", got, err)
	}
}

var _ = models.DefaultClusterID

func nodeIDs(nodes []Node) []uint {
	var out []uint
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}

func TestNodesListsOnlyTheLocationsNodes(t *testing.T) {
	s, db := newPlacement(t, map[uint]bool{10: true})
	db.Model(&serverTable{}).Where("id = ?", 10).Update("swarm_node_id", "sw-10")

	got, err := s.Nodes(5, "eu-east", false)
	if err != nil {
		t.Fatal(err)
	}
	if ids := nodeIDs(got); fmt.Sprint(ids) != "[10 11 12 13]" {
		t.Fatalf("eu-east nodes = %v, want 10-13", ids)
	}
	if !got[0].Online || got[1].Online || !got[3].Cordoned || got[0].SwarmNodeID != "sw-10" {
		t.Errorf("node state not reported: %+v", got)
	}

	if def, err := s.Nodes(5, "", false); err != nil || fmt.Sprint(nodeIDs(def)) != "[1]" {
		t.Errorf("unstated location = %v (%v), want the default cluster's node", nodeIDs(def), err)
	}
	if _, err := s.Nodes(5, "gpu", false); !errors.Is(err, ErrLocationNotFound) {
		t.Errorf("restricted location err = %v, want ErrLocationNotFound", err)
	}
	if got, err := s.Nodes(5, "gpu", true); err != nil || fmt.Sprint(nodeIDs(got)) != "[20]" {
		t.Errorf("restricted location for an admin = %v (%v)", nodeIDs(got), err)
	}

	s.SetPolicy(fakePolicy{placement: models.PlanPlacement{Pool: "pro"}})
	if got, err := s.Nodes(5, "eu-east", false); err != nil || fmt.Sprint(nodeIDs(got)) != "[12]" {
		t.Errorf("pooled plan = %v (%v), want only the pro node", nodeIDs(got), err)
	}
}

func TestNodePin(t *testing.T) {
	s, db := newPlacement(t, map[uint]bool{})
	// The control-plane node may carry cluster id 0, which stands for the default cluster.
	db.Model(&serverTable{}).Where("id = ?", 1).Update("cluster_id", 0)

	if _, err := s.Place(Request{WorkspaceID: 5, ServerID: 1, Location: "default", Admin: true}); err != nil {
		t.Errorf("pin to the control-plane node in the default location: %v", err)
	}
	if _, err := s.Place(Request{WorkspaceID: 5, ServerID: 13, Admin: true}); !errors.Is(err, ErrNoSchedulableNode) {
		t.Errorf("pin to a cordoned node err = %v, want ErrNoSchedulableNode", err)
	}
	if _, err := s.Place(Request{WorkspaceID: 5, Colocate: 13}); err != nil {
		t.Errorf("colocation follows a resource already on a cordoned node: %v", err)
	}
}

func TestNodeConstraint(t *testing.T) {
	s, db := newPlacement(t, map[uint]bool{})
	db.Model(&serverTable{}).Where("id IN ?", []uint{1, 10}).Update("swarm_node_id", "sw")

	if got := s.NodeConstraint(10); got != "node.id==sw" {
		t.Errorf("swarm member = %q", got)
	}
	if got := s.NodeConstraint(0); got != "node.id==sw" {
		t.Errorf("server id 0 = %q, want the control-plane node's", got)
	}
	if got := s.NodeConstraint(11); got != "" {
		t.Errorf("non-member = %q, want none", got)
	}
}

// The control-plane node may carry cluster id 0. The organization check must resolve it to the default
// cluster, or a pin or colocation onto that node skips it.
func TestOrganizationCheckCoversTheControlPlaneNode(t *testing.T) {
	s, db := newPlacement(t, map[uint]bool{})
	db.Model(&serverTable{}).Where("id = ?", 1).Update("cluster_id", 0)
	acme := uint(7)
	db.Model(&clusterTable{}).Where("id = ?", 3).Updates(map[string]any{"organization_id": acme, "visibility": "organization"})
	s.SetOrgs(fakeOrgs{ofWorkspace: map[uint]uint{5: acme}, owners: map[uint]bool{acme: true}})

	if _, err := s.Place(Request{WorkspaceID: 5, ServerID: 1, Admin: true}); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("confined workspace pinned to the control-plane node: err = %v, want ErrLocationNotAllowed", err)
	}
	if _, err := s.Place(Request{WorkspaceID: 5, Colocate: 1}); !errors.Is(err, ErrLocationNotAllowed) {
		t.Errorf("confined workspace colocated on the control-plane node: err = %v, want ErrLocationNotAllowed", err)
	}
}

// Following an existing resource while naming a location must not confirm that another organization's
// location exists: it reads as no such location, exactly like naming it directly.
func TestColocationDoesNotConfirmForeignLocations(t *testing.T) {
	s, db := newPlacement(t, map[uint]bool{})
	db.Model(&clusterTable{}).Where("id = ?", 3).Updates(map[string]any{"organization_id": 7, "visibility": "organization"})
	s.SetOrgs(fakeOrgs{ofWorkspace: map[uint]uint{5: 2}})

	if _, err := s.Place(Request{WorkspaceID: 5, Colocate: 10, Location: "gpu"}); !errors.Is(err, ErrLocationNotFound) {
		t.Errorf("colocation naming a foreign location: err = %v, want ErrLocationNotFound", err)
	}
	if _, err := s.Place(Request{WorkspaceID: 5, Colocate: 10, Location: "default"}); !errors.Is(err, ErrLocationMismatch) {
		t.Errorf("colocation naming another shared location: err = %v, want ErrLocationMismatch", err)
	}
}
