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
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Off means off, even in the system workspace.
func TestGrantsRefusedWhenTheSwitchIsOff(t *testing.T) {
	s := &Service{} // grantsEnabled false, as a platform that never opted in

	if _, err := s.validCapabilities(1, []string{"NET_ADMIN"}); !errors.Is(err, models.ErrGrantsDisabled) {
		t.Errorf("capabilities: err = %v, want ErrGrantsDisabled", err)
	}
	if _, err := s.validDevices(1, []string{"/dev/net/tun"}); !errors.Is(err, models.ErrGrantsDisabled) {
		t.Errorf("devices: err = %v, want ErrGrantsDisabled", err)
	}
}

// The switch is checked before the workspace is read, so no workspace flag can reach past it.
func TestNoWorkspaceCanPassASwitchThatIsOff(t *testing.T) {
	s := &Service{} // no workspace repository wired at all
	for _, tier := range []models.CapabilityTier{models.TierCommon, models.TierElevated} {
		if err := s.grantAllowed(1, tier); !errors.Is(err, models.ErrGrantsDisabled) {
			t.Errorf("tier %d: err = %v, want ErrGrantsDisabled", tier, err)
		}
	}
}

func TestNoGrantsRequestedIsFineWithTheSwitchOff(t *testing.T) {
	s := &Service{}
	caps, err := s.validCapabilities(1, nil)
	if err != nil || len(caps) != 0 {
		t.Errorf("no capabilities = %v, %v; want empty and no error", caps, err)
	}
	devices, err := s.validDevices(1, []string{})
	if err != nil || len(devices) != 0 {
		t.Errorf("no devices = %v, %v; want empty and no error", devices, err)
	}
}
