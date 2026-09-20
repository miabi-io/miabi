// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Dedicating a location hides it from every other tenant, so it is refused while other
// organizations still run workloads there — they would keep running, unreachable from the
// workspace that owns them. Releasing it is always allowed and returns it to shared.
func TestDedicatingRefusesForeignWorkloads(t *testing.T) {
	store := &memStore{
		def:      models.Cluster{ID: 1, Name: "default", IsDefault: true, Visibility: models.ClusterVisibilityAll},
		others:   map[uint]models.Cluster{2: {ID: 2, Name: "gpu", Visibility: models.ClusterVisibilityAll}},
		foreign:  map[uint]int64{2: 3},
		assigned: map[uint]uint{},
	}
	s := &Service{store: store, states: map[uint]swarmState{}}

	acme := uint(7)
	if _, err := s.UpdateCluster(2, ClusterPatch{OrganizationID: &acme, Acknowledge: true}); !errors.Is(err, ErrClusterHasForeignWorkloads) {
		t.Fatalf("dedicating a busy location: err = %v, want ErrClusterHasForeignWorkloads", err)
	}

	store.foreign[2] = 0
	c, err := s.UpdateCluster(2, ClusterPatch{OrganizationID: &acme, Acknowledge: true})
	if err != nil {
		t.Fatalf("dedicating an empty location: %v", err)
	}
	if c.OrganizationID == nil || *c.OrganizationID != acme {
		t.Errorf("organization = %v, want %d", c.OrganizationID, acme)
	}
	if c.Visibility != models.ClusterVisibilityOrganization {
		t.Errorf("visibility = %q, want organization", c.Visibility)
	}
	if store.cleared == 0 {
		t.Error("dedicating a location must clear the workspace defaults it invalidated")
	}

	// Releasing is never blocked: the workloads it would strand are the owner's own.
	store.foreign[2] = 5
	none := uint(0)
	c, err = s.UpdateCluster(2, ClusterPatch{OrganizationID: &none, Acknowledge: true})
	if err != nil {
		t.Fatalf("releasing: %v", err)
	}
	if c.OrganizationID != nil || c.Visibility != models.ClusterVisibilityAll {
		t.Errorf("released cluster = org %v / visibility %q, want nil / all", c.OrganizationID, c.Visibility)
	}
}
