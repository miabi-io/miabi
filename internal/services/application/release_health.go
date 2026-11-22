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

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

// ReleaseHealth reports one release's runtime state, so a stable and a canary release can be shown side by side.
func (s *Service) ReleaseHealth(ctx context.Context, app *models.Application, rel *models.Release) models.ReleaseHealth {
	h := models.ReleaseHealth{Status: string(app.Status)}
	if app.RuntimeKind == models.RuntimeService {
		mgr, err := s.swarmManager(ctx, app)
		if err != nil {
			return h
		}
		target := node.AppAlias(app)
		if rel != nil && rel.ContainerID != "" {
			target = rel.ContainerID
		}
		st, err := mgr.ServiceInspect(ctx, target)
		if err != nil {
			h.Status = "no_container"
			return h
		}
		h.Running, h.Desired = st.RunningTasks, st.Replicas
		switch {
		case app.Status.StopIntended() || st.Replicas == 0:
			h.Status = "stopped"
		case st.RunningTasks >= st.Replicas:
			h.Status = "running"
		default:
			h.Status = "starting"
		}
		return h
	}
	if rel == nil || rel.ContainerID == "" {
		h.Status = "no_container"
		return h
	}
	if !app.Status.StopIntended() {
		h.Desired = 1
	}
	c, err := s.eng(app).InspectContainer(ctx, rel.ContainerID)
	if err != nil {
		h.Status = "no_container"
		return h
	}
	if c.State == "running" {
		h.Running = 1
	}
	h.Status = deriveLiveStatus(c.State, c.Health, c.Restarting || c.State == "restarting", app.Status.StopIntended())
	return h
}
