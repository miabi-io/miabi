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

package cluster

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func orgID(n uint) *uint { return &n }

func dedicationService(others map[uint]models.Cluster) *Service {
	store := &memStore{
		def:      models.Cluster{ID: 1, Name: "default", IsDefault: true, Visibility: models.ClusterVisibilityAll},
		others:   others,
		assigned: map[uint]uint{},
	}
	return &Service{
		store:  store,
		states: map[uint]swarmState{},
		orgLabels: func(ids []uint) map[uint]string {
			all := map[uint]string{7: "Acme", 8: "Globex"}
			out := map[uint]string{}
			for _, id := range ids {
				if v, ok := all[id]; ok {
					out[id] = v
				}
			}
			return out
		},
	}
}

func findCluster(list []models.Cluster, id uint) *models.Cluster {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// Dedication is derived rather than stored, so listing is where it has to be right.
func TestClusters_MarksDedication(t *testing.T) {
	s := dedicationService(map[uint]models.Cluster{
		2: {ID: 2, Name: "gpu", Visibility: models.ClusterVisibilityOrganization, OrganizationID: orgID(7)},
		3: {ID: 3, Name: "shared", Visibility: models.ClusterVisibilityAll},
	})

	list, err := s.Clusters()
	if err != nil {
		t.Fatalf("Clusters: %v", err)
	}
	gpu := findCluster(list, 2)
	if gpu == nil || !gpu.Dedicated || gpu.OrganizationName != "Acme" {
		t.Fatalf("owned cluster = %+v, want dedicated to Acme", gpu)
	}
	if shared := findCluster(list, 3); shared == nil || shared.Dedicated || shared.OrganizationName != "" {
		t.Fatalf("unowned cluster = %+v, want not dedicated", shared)
	}
}

// Restricted means admin-only placement, not organization ownership.
func TestClusters_RestrictedIsNotDedicated(t *testing.T) {
	s := dedicationService(map[uint]models.Cluster{
		2: {ID: 2, Name: "staging", Visibility: models.ClusterVisibilityRestricted},
	})

	list, err := s.Clusters()
	if err != nil {
		t.Fatalf("Clusters: %v", err)
	}
	if c := findCluster(list, 2); c == nil || c.Dedicated {
		t.Fatalf("restricted cluster = %+v, want not dedicated", c)
	}
}

// The fact is the owner id; the name is decoration.
func TestClusters_DedicatedWithoutLabels(t *testing.T) {
	s := dedicationService(map[uint]models.Cluster{
		2: {ID: 2, Name: "gpu", Visibility: models.ClusterVisibilityOrganization, OrganizationID: orgID(7)},
	})
	s.orgLabels = nil

	list, err := s.Clusters()
	if err != nil {
		t.Fatalf("Clusters: %v", err)
	}
	c := findCluster(list, 2)
	if c == nil || !c.Dedicated || c.OrganizationName != "" {
		t.Fatalf("cluster = %+v, want dedicated and unnamed", c)
	}
}

// A node holds no organization of its own, so moving it to a shared cluster must clear dedication.
func TestEnrich_NodesInheritClusterDedication(t *testing.T) {
	s := dedicationService(map[uint]models.Cluster{
		2: {ID: 2, Name: "gpu", Visibility: models.ClusterVisibilityOrganization, OrganizationID: orgID(8)},
		3: {ID: 3, Name: "shared", Visibility: models.ClusterVisibilityAll},
	})

	servers := []models.Server{
		{ID: 10, ClusterID: 2},
		{ID: 11, ClusterID: 3},
		{ID: 12, ClusterID: 1}, // the default cluster, owned by nobody
	}
	s.Enrich(servers)

	if !servers[0].Dedicated || servers[0].OrganizationName != "Globex" {
		t.Errorf("node in an owned cluster = %+v, want dedicated to Globex", servers[0])
	}
	for _, srv := range servers[1:] {
		if srv.Dedicated || srv.OrganizationName != "" {
			t.Errorf("node %d = %+v, want not dedicated", srv.ID, srv)
		}
	}
}

func TestEnrich_DefaultClusterSentinel(t *testing.T) {
	s := dedicationService(nil)
	store := s.store.(*memStore)
	store.def.Visibility = models.ClusterVisibilityOrganization
	store.def.OrganizationID = orgID(7)

	servers := []models.Server{{ID: 10, ClusterID: models.DefaultClusterID}, {ID: 11, ClusterID: store.def.ID}}
	s.Enrich(servers)

	for _, srv := range servers {
		if !srv.Dedicated || srv.OrganizationName != "Acme" {
			t.Errorf("node %d = %+v, want dedicated to Acme", srv.ID, srv)
		}
	}
}

// The previous owner's default may have been this very cluster.
func TestUpdateCluster_RealignsOrganizationDefaults(t *testing.T) {
	type call struct{ org, prefer uint }
	var calls []call

	s := dedicationService(map[uint]models.Cluster{
		2: {ID: 2, Name: "gpu", Visibility: models.ClusterVisibilityAll},
	})
	s.orgAligner = func(orgID, prefer uint) error {
		calls = append(calls, call{orgID, prefer})
		return nil
	}

	acme := uint(7)
	if _, err := s.UpdateCluster(2, ClusterPatch{OrganizationID: &acme, Acknowledge: true}); err != nil {
		t.Fatalf("dedicating: %v", err)
	}
	if len(calls) != 1 || calls[0] != (call{org: 7, prefer: 2}) {
		t.Fatalf("dedicating called %+v, want one align of org 7 preferring cluster 2", calls)
	}

	calls = nil
	globex := uint(8)
	if _, err := s.UpdateCluster(2, ClusterPatch{OrganizationID: &globex, Acknowledge: true}); err != nil {
		t.Fatalf("reassigning: %v", err)
	}
	if len(calls) != 2 || calls[0] != (call{org: 8, prefer: 2}) || calls[1] != (call{org: 7}) {
		t.Fatalf("reassigning called %+v, want the new owner then the previous one", calls)
	}

	calls = nil
	none := uint(0)
	if _, err := s.UpdateCluster(2, ClusterPatch{OrganizationID: &none, Acknowledge: true}); err != nil {
		t.Fatalf("releasing: %v", err)
	}
	if len(calls) != 1 || calls[0] != (call{org: 8}) {
		t.Fatalf("releasing called %+v, want one align of the previous owner", calls)
	}
}

// Anything but an ownership change leaves the organizations alone.
func TestUpdateCluster_NoRealignWithoutAnOwnershipChange(t *testing.T) {
	var called int
	s := dedicationService(map[uint]models.Cluster{2: {ID: 2, Name: "gpu", Visibility: models.ClusterVisibilityAll}})
	s.orgAligner = func(uint, uint) error { called++; return nil }

	name := "GPU"
	if _, err := s.UpdateCluster(2, ClusterPatch{DisplayName: &name}); err != nil {
		t.Fatalf("renaming: %v", err)
	}
	if called != 0 {
		t.Fatalf("a rename realigned %d organization(s), want none", called)
	}
}

// Ownership decides who may see and place, so it must not change as a form side effect.
func TestUpdateCluster_DedicationNeedsAcknowledgement(t *testing.T) {
	s := dedicationService(map[uint]models.Cluster{
		2: {ID: 2, Name: "gpu", Visibility: models.ClusterVisibilityAll},
	})

	acme := uint(7)
	if _, err := s.UpdateCluster(2, ClusterPatch{OrganizationID: &acme}); !errors.Is(err, ErrDedicationAckRequired) {
		t.Fatalf("unacknowledged dedication: err = %v, want ErrDedicationAckRequired", err)
	}
	c, err := s.Cluster(2)
	if err != nil {
		t.Fatal(err)
	}
	if c.OrganizationID != nil {
		t.Fatalf("a refused change must not have been written, got org %v", c.OrganizationID)
	}
	if _, err := s.UpdateCluster(2, ClusterPatch{OrganizationID: &acme, Acknowledge: true}); err != nil {
		t.Fatalf("acknowledged dedication: %v", err)
	}
}

func TestUpdateCluster_SameOwnerIsNotAChange(t *testing.T) {
	acme := uint(7)
	s := dedicationService(map[uint]models.Cluster{
		2: {ID: 2, Name: "gpu", Visibility: models.ClusterVisibilityOrganization, OrganizationID: &acme},
	})
	var aligned int
	s.orgAligner = func(uint, uint) error { aligned++; return nil }

	if _, err := s.UpdateCluster(2, ClusterPatch{OrganizationID: &acme}); err != nil {
		t.Fatalf("re-sending the same owner: %v", err)
	}
	if aligned != 0 {
		t.Fatalf("realigned %d organization(s) for a no-op, want none", aligned)
	}
}

// Regression: the stranding check only ran for unowned locations, so reassignment bypassed it.
func TestUpdateCluster_ReassignmentChecksForeignWorkloads(t *testing.T) {
	acme := uint(7)
	s := dedicationService(map[uint]models.Cluster{
		2: {ID: 2, Name: "gpu", Visibility: models.ClusterVisibilityOrganization, OrganizationID: &acme},
	})
	s.store.(*memStore).foreign = map[uint]int64{2: 4} // Acme's, from the incoming owner's side

	globex := uint(8)
	_, err := s.UpdateCluster(2, ClusterPatch{OrganizationID: &globex, Acknowledge: true})
	if !errors.Is(err, ErrClusterHasForeignWorkloads) {
		t.Fatalf("reassigning a busy location: err = %v, want ErrClusterHasForeignWorkloads", err)
	}
}

func TestDedication_BreaksDownByOrganization(t *testing.T) {
	s := dedicationService(map[uint]models.Cluster{2: {ID: 2, Name: "gpu"}})
	s.store.(*memStore).byOrg = map[uint]int64{7: 2, 8: 9, 0: 1}

	impact, err := s.Dedication(2)
	if err != nil {
		t.Fatalf("Dedication: %v", err)
	}
	if impact.Workloads != 12 {
		t.Errorf("total = %d, want 12", impact.Workloads)
	}
	if len(impact.Tenants) != 3 {
		t.Fatalf("tenants = %+v, want 3", impact.Tenants)
	}
	if impact.Tenants[0].OrganizationID != 8 || impact.Tenants[0].Name != "Globex" || impact.Tenants[0].Workloads != 9 {
		t.Errorf("largest tenant = %+v, want Globex with 9", impact.Tenants[0])
	}
	if impact.Tenants[2].OrganizationID != 0 || impact.Tenants[2].Name != "" {
		t.Errorf("last tenant = %+v, want the unowned workspaces, unnamed", impact.Tenants[2])
	}
}
