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
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/netalloc"
)

const overlayDriver = "overlay"

// nil leaves the addressing to Swarm.
func (s *Service) SetAllocator(a *netalloc.Service) { s.alloc = a }

// ClusterOfServer returns the cluster a node belongs to; 0 is the local node, always in the default cluster.
func (s *Service) ClusterOfServer(serverID uint) uint {
	if serverID == 0 || serverID == s.clients.LocalID() {
		return s.defaultID()
	}
	srv, err := s.nodes.Get(serverID)
	if err != nil {
		return models.DefaultClusterID
	}
	return srv.ClusterID
}

// LocationLabel names a cluster the way tenants see it.
func (s *Service) LocationLabel(clusterID uint) string {
	if s.store != nil {
		if c, err := s.store.FindByID(clusterID); err == nil {
			return c.Label()
		}
	}
	return fmt.Sprintf("location %d", clusterID)
}

// A remote swarm only takes empty nodes, so its workspace networks are overlays from the start.
func (s *Service) WorkspaceOverlay(clusterID uint, n models.Network) bool {
	if n.Driver == overlayDriver {
		return true
	}
	return !s.isDefault(clusterID) && s.IsSwarm(clusterID)
}

// The default cluster's overlays are created by the network service.
func (s *Service) EnsureWorkspaceOverlay(ctx context.Context, clusterID uint, n models.Network) error {
	if s.isDefault(clusterID) || !s.IsSwarm(clusterID) {
		return nil
	}
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return err
	}
	spec := docker.NetworkSpec{Name: n.DockerName, Driver: overlayDriver, Attachable: true, Encrypted: true, Internal: n.Internal}
	if s.alloc != nil {
		_, _, err = s.alloc.EnsureManaged(ctx, mgr, spec, 0, models.NetAllocKindOverlay)
	} else {
		_, err = mgr.EnsureNetworkSpec(ctx, spec)
	}
	if err != nil {
		return fmt.Errorf("ensure overlay network %s: %w", n.DockerName, err)
	}
	return nil
}
