// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// SetAllocator wires the subnet pool a remote swarm's workspace overlays are carved from (nil-safe; nil
// leaves the addressing to Swarm).
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

// WorkspaceOverlay reports whether a workspace network is an overlay in a cluster. In the default cluster its
// record decides; a remote swarm only takes empty nodes, so its workspace networks are overlays from the start.
func (s *Service) WorkspaceOverlay(clusterID uint, n models.Network) bool {
	if n.Driver == overlayDriver {
		return true
	}
	return !s.isDefault(clusterID) && s.IsSwarm(clusterID)
}

// EnsureWorkspaceOverlay creates a workspace network as an overlay in a remote swarm, on the network's pool
// subnet, so a container or database placed there can attach. The default cluster's overlays are created by
// the network service.
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
