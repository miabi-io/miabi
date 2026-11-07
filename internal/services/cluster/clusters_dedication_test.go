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

// Other tenants' workloads would keep running but become unreachable from their workspaces.
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
