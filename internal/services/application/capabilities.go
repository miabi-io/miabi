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
	"github.com/miabi-io/miabi/internal/models"
)

// Dropping is never gated: it only takes privileges away.
func validDropCapabilities(add, drop []string) ([]string, error) {
	caps, err := models.NormalizeDropCapabilities(drop)
	if err != nil {
		return nil, err
	}
	if len(caps) == 0 {
		return nil, nil
	}
	if err := models.CheckCapabilityConflict(add, caps); err != nil {
		return nil, err
	}
	return caps, nil
}

func (s *Service) EnforcesNoNewPrivileges(workspaceID uint) bool {
	return s.quota.RequireNonRootUser(workspaceID, false)
}

// validCapabilities normalizes and gates an application's extra Linux capabilities.
func (s *Service) validCapabilities(workspaceID uint, in []string) ([]string, error) {
	caps, err := models.NormalizeCapabilities(in)
	if err != nil {
		return nil, err
	}
	if len(caps) == 0 {
		return nil, nil
	}
	if err := s.grantAllowed(workspaceID, models.HighestCapabilityTier(caps)); err != nil {
		return nil, err
	}
	return caps, nil
}

// validDevices normalizes and gates an application's host devices.
func (s *Service) validDevices(workspaceID uint, in []string) ([]string, error) {
	devices, err := models.NormalizeDevices(in)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, nil
	}
	if err := s.grantAllowed(workspaceID, models.HighestDeviceTier(devices)); err != nil {
		return nil, err
	}
	return devices, nil
}

// SetGrantsEnabled sets the platform master switch (MIABI_CONTAINER_GRANTS_ENABLED).
func (s *Service) SetGrantsEnabled(on bool) { s.grantsEnabled = on }

// grantAllowed decides whether this workspace may hold a grant of the given tier.
func (s *Service) grantAllowed(workspaceID uint, tier models.CapabilityTier) error {
	// Before the workspace is read: no workspace flag may reach past this.
	if !s.grantsEnabled {
		return models.ErrGrantsDisabled
	}
	if s.workspaces == nil {
		return models.ErrCapabilityElevated
	}
	ws, err := s.workspaces.FindByID(workspaceID)
	if err != nil {
		return err
	}
	// A per-app grant must not add back what the profile exists to drop.
	if s.quota != nil && s.quota.RequireNonRootUser(workspaceID, false) {
		return models.ErrCapabilityRestricted
	}
	if !ws.Privileged {
		return ErrHostMountNotPrivileged
	}
	if tier >= models.TierElevated && !ws.System {
		return models.ErrCapabilityElevated
	}
	return nil
}
