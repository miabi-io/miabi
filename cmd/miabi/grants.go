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

package main

import (
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/worker"
)

// Both processes deploy, so both wire the deploy-time grant re-check.
func newGrantGuard(enabled bool, workspaces *repositories.WorkspaceRepository, q *quota.Service) worker.GrantGuard {
	return worker.GrantGuardFunc(func(workspaceID uint, tier models.CapabilityTier) error {
		// An app configured while grants were enabled must stop receiving them.
		if !enabled {
			return models.ErrGrantsDisabled
		}
		if q != nil && q.RequireNonRootUser(workspaceID, false) {
			return models.ErrCapabilityRestricted
		}
		ws, err := workspaces.FindByID(workspaceID)
		if err != nil {
			return err
		}
		if !ws.Privileged {
			return models.ErrCapabilityElevated
		}
		if tier >= models.TierElevated && !ws.System {
			return models.ErrCapabilityElevated
		}
		return nil
	})
}
