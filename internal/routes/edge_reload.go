// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"context"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/node"
)

// edgeReloader implements route.EdgeReloader: for each affected node it resolves the gateway
// address and token and tells the edge gateway to pull its config immediately. Best-effort —
// a node still converges on its next provider poll.
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
