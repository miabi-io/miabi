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
)

// SettleTransitional finishes a stop or delete a crash interrupted, so the app never stays stopping or deleting.
func (s *Service) SettleTransitional(ctx context.Context, app *models.Application) error {
	switch app.Status {
	case models.AppStatusStopping:
		next := models.AppStatusStopped
		// The stop never reached Docker, so the app is still what it was before.
		if s.liveStatus(ctx, app).Running {
			next = models.AppStatusRunning
		}
		logger.Info("settling an interrupted stop", "app", app.Name, "app_id", app.ID, "status", next)
		return s.apps.SetStatus(app.ID, next)
	case models.AppStatusDeleting:
		logger.Info("resuming an interrupted delete", "app", app.Name, "app_id", app.ID)
		return s.Delete(ctx, app)
	}
	return nil
}
