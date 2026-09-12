// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// SyncPoolLabel mirrors a node's pool onto its swarm node, where service placement constraints read it. A no-op
// for a node outside an active swarm.
func (s *Service) SyncPoolLabel(ctx context.Context, srv *models.Server) error {
	if srv == nil || srv.SwarmNodeID == "" || !s.IsSwarm(srv.ClusterID) {
		return nil
	}
	mgr, err := s.Manager(ctx, srv.ClusterID)
	if err != nil {
		return err
	}
	return mgr.SwarmNodeSetLabel(ctx, srv.SwarmNodeID, models.PoolLabel, models.PoolOf(srv))
}

// reassertPoolLabels labels every pooled swarm member, covering a node that joined after its pool was set or a
// pool set while its swarm was unreachable.
func (s *Service) reassertPoolLabels(ctx context.Context) {
	servers, err := s.nodes.List(ctx)
	if err != nil {
		return
	}
	for i := range servers {
		if models.PoolOf(&servers[i]) == "" {
			continue
		}
		if err := s.SyncPoolLabel(ctx, &servers[i]); err != nil {
			logger.Warn("failed to label a swarm node with its pool", "node", servers[i].Name, "error", err)
		}
	}
}
