// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"context"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

// ApplyServiceEndpointMode switches the running service apps of a cluster to how the cluster reaches services by
// name. Tasks keep running; a service that cannot be switched now takes the mode on its next deploy.
func (s *Service) ApplyServiceEndpointMode(ctx context.Context, clusterID uint, mode models.ServiceEndpointMode) {
	apps, err := s.apps.ListByCluster(clusterID)
	if err != nil {
		logger.Warn("service endpoint mode: listing the cluster's apps failed", "cluster", clusterID, "error", err)
		return
	}
	for i := range apps {
		app := &apps[i]
		if app.RuntimeKind != models.RuntimeService || app.CurrentReleaseID == nil {
			continue
		}
		mgr, err := s.swarmManager(ctx, app)
		if err != nil {
			logger.Warn("service endpoint mode: the cluster's manager is unreachable", "cluster", clusterID, "error", err)
			return
		}
		if err := mgr.ServiceSetEndpointMode(ctx, node.AppAlias(app), string(mode)); err != nil {
			logger.Warn("service endpoint mode: switching a service failed", "app", app.ID, "mode", mode, "error", err)
		}
	}
}
