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

package quota

import (
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
)

// flagPlatformRunners is a local copy of enterprise.FlagPlatformRunners.
const flagPlatformRunners = "platform_runners"

// RunnerBinding never applies to a workspace's own runners; an empty Allowed offers the whole shared pool.
type RunnerBinding struct {
	Allowed []string
}

func (b RunnerBinding) Allows(name string) bool {
	if len(b.Allowed) == 0 {
		return true
	}
	for _, a := range b.Allowed {
		if a == name {
			return true
		}
	}
	return false
}

// False when enforcement is off or unlicensed, so a lapsed license leaves the list inert rather than stranding workspaces.
func (s *Service) EffectivePlatformRunners(workspaceID uint) (RunnerBinding, bool) {
	if !s.Enabled() || !s.entitled(flagPlatformRunners) {
		return RunnerBinding{}, false
	}
	var o *models.WorkspaceQuota
	if s.overrides != nil {
		o, _ = s.overrides.FindByWorkspace(workspaceID)
	}
	return RunnerBinding{Allowed: platformRunnersFrom(s.effectivePlan(workspaceID), o)}, true
}

func platformRunnersFrom(p *models.Plan, o *models.WorkspaceQuota) []string {
	if o != nil && o.PlatformRunners != nil {
		return *o.PlatformRunners
	}
	if p == nil {
		return nil
	}
	return p.PlatformRunners
}

// The admin UI asks so it can explain a locked field.
func (s *Service) PlatformRunnersLicensed() bool { return s != nil && s.entitled(flagPlatformRunners) }

func (s *Service) RequirePlatformRunner(workspaceID uint, name string) error {
	binding, bound := s.EffectivePlatformRunners(workspaceID)
	if !bound || binding.Allows(name) {
		return nil
	}
	return &codedError{
		code: "CAPABILITY_DENIED",
		msg:  fmt.Sprintf("capability not allowed by plan: platform runner %q", name),
		base: ErrCapabilityDenied,
	}
}
