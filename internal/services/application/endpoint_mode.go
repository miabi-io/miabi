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

package application

import (
	"context"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

// Tasks keep running; a service that cannot be switched now takes the mode on its next deploy.
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
