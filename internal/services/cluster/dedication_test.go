// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func orgID(n uint) *uint { return &n }

// dedicationService builds a service over a default cluster plus the given ones, with an
// organization label resolver that names 7 and 8.
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

// A cluster is dedicated exactly when it names an owning organization. The flag is derived here
// rather than stored, so listing is where it has to be right.
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

// Restricted means admin-only placement, not organization ownership. Reading dedication off
// visibility instead of the owner would label it as belonging to an organization it does not.
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

// Without a label resolver a cluster is still dedicated, just unnamed: the fact is the owner id,
// and the name is decoration.
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

// A node inherits its cluster's dedication. It holds no organization of its own, so moving it to a
// shared cluster has to stop reporting it as dedicated.
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

// A node recorded against the default cluster by its sentinel id resolves to the same cluster as
// one recorded by its real id, so dedicating the default cluster reaches both.
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
