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

package network

import (
	"context"
	"errors"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"gorm.io/gorm"
)

// PruneStaleBridges removes leftover workspace bridges that would make overlay attachments fail with "Pool overlaps".
func (s *Service) PruneStaleBridges(ctx context.Context, dc docker.Client, nodeName string) []string {
	if !s.clusterOn() || dc == nil {
		return nil
	}
	nets, err := dc.ListNetworks(ctx)
	if err != nil {
		logger.Warn("stale bridge cleanup: could not list networks", "node", nodeName, "error", err)
		return nil
	}
	var removed []string
	for _, n := range nets {
		if !isWorkspaceBridge(n) {
			continue
		}
		if _, err := s.repo.FindByDockerName(n.Name); !errors.Is(err, gorm.ErrRecordNotFound) {
			continue // still a workspace network, or the lookup failed: leave it
		}
		if err := dc.RemoveNetwork(ctx, n.Name); err != nil {
			logger.Warn("leftover workspace bridge is still in use; overlay attachments on this node fail while it overlaps their subnet",
				"node", nodeName, "network", n.Name, "subnet", n.Subnet, "error", err)
			continue
		}
		logger.Info("removed leftover workspace bridge", "node", nodeName, "network", n.Name, "subnet", n.Subnet)
		removed = append(removed, n.Name)
	}
	return removed
}

// isWorkspaceBridge reports whether a network is a node-local workspace bridge Miabi created.
func isWorkspaceBridge(n docker.Network) bool {
	return n.Driver == DriverBridge &&
		n.Labels[docker.ManagedLabel] == "true" &&
		strings.HasPrefix(n.Name, "mb-ws") &&
		!strings.HasSuffix(n.Name, overlaySuffix)
}
