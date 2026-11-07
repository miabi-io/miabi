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
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/node"
)

// Best-effort: a node still converges on its next provider poll.
type edgeReloader struct {
	nodes *node.Service
	gw    *edgegateway.Service
}

func newEdgeReloader(nodes *node.Service, gw *edgegateway.Service) edgeReloader {
	return edgeReloader{nodes: nodes, gw: gw}
}

func (e edgeReloader) ReloadServers(ctx context.Context, serverIDs []uint) {
	for _, id := range serverIDs {
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
