// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/worker"
)

// newGrantGuard re-checks grants at deploy time against the rules the application
// service applies at write time. Both processes deploy, so both wire it.
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
