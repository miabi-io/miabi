// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// The switch is off unless an operator turns it on, and off means off — including
// in the system workspace, which every other rule in this file treats as the most
// privileged place on the platform.
func TestGrantsRefusedWhenTheSwitchIsOff(t *testing.T) {
	s := &Service{} // grantsEnabled false, as a platform that never opted in

	if _, err := s.validCapabilities(1, []string{"NET_ADMIN"}); !errors.Is(err, models.ErrGrantsDisabled) {
		t.Errorf("capabilities: err = %v, want ErrGrantsDisabled", err)
	}
	if _, err := s.validDevices(1, []string{"/dev/net/tun"}); !errors.Is(err, models.ErrGrantsDisabled) {
		t.Errorf("devices: err = %v, want ErrGrantsDisabled", err)
	}
}

// The switch is checked before the workspace is even read, so no workspace flag
// can reach past it.
func TestNoWorkspaceCanPassASwitchThatIsOff(t *testing.T) {
	s := &Service{} // no workspace repository wired at all
	for _, tier := range []models.CapabilityTier{models.TierCommon, models.TierElevated} {
		if err := s.grantAllowed(1, tier); !errors.Is(err, models.ErrGrantsDisabled) {
			t.Errorf("tier %d: err = %v, want ErrGrantsDisabled", tier, err)
		}
	}
}

// An app that asks for nothing is unaffected, which is every app on a platform
// that leaves the switch alone.
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
