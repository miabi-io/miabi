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

package routes

import (
	"context"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/gatewayinstance"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Best-effort: a node still converges on its next provider poll.
type edgeReloader struct {
	nodes     *node.Service
	gw        *edgegateway.Service
	instances *gatewayinstance.Service
}

func newEdgeReloader(nodes *node.Service, gw *edgegateway.Service, instances *gatewayinstance.Service) edgeReloader {
	return edgeReloader{nodes: nodes, gw: gw, instances: instances}
}

// Every gateway that polled the cluster lately is reloaded, not only the one the route change names.
func (e edgeReloader) ReloadServers(ctx context.Context, serverIDs []uint) {
	seen := make(map[uint]bool, len(serverIDs))
	var targets []uint
	add := func(id uint) {
		if !seen[id] {
			seen[id] = true
			targets = append(targets, id)
		}
	}
	for _, id := range serverIDs {
		add(id)
		if srv, err := e.nodes.Get(id); err == nil && srv != nil {
			for _, peer := range e.instances.RecentNodes(ctx, srv.ClusterID) {
				add(peer)
			}
		}
	}
	for _, id := range targets {
		srv, err := e.nodes.Get(id)
		if err != nil || srv == nil {
			continue
		}
		token, err := e.nodes.GatewayToken(id)
		if err != nil || token == "" {
			logger.Warn("edge gateway reload skipped: no token", "server", id, "err", err)
			continue
		}
		if err := e.gw.Reload(ctx, srv, token); err != nil {
			logger.Warn("edge gateway reload failed", "server", srv.Name, "err", err)
			continue
		}
		logger.Debug("edge gateway reloaded on demand", "server", srv.Name)
	}
}

// bundleKeys mirrors the keys the provider endpoints encrypt under, so a status checksum matches a served one.
type bundleKeys struct {
	servers *repositories.ServerRepository
}

func (bundleKeys) ClusterKey(c *models.Cluster) string {
	return edgegateway.ClusterConfigKey(c, proxy.CentralConfigKey())
}

func (k bundleKeys) NodeKey(serverID uint) string {
	srv, err := k.servers.FindByID(serverID)
	if err != nil {
		return ""
	}
	return edgegateway.ConfigKey(srv, proxy.CentralConfigKey())
}
