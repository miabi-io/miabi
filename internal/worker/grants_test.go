// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// A grant that outlived its authorisation must stop the deploy, not keep being
// applied. Write-time validation cannot cover this: a workspace can lose its
// privileged flag and never be written to again.
func TestDeployRefusesGrantsTheWorkspaceLost(t *testing.T) {
	revoked := errors.New("workspace is no longer privileged")
	b := &runtimeBuilder{grants: GrantGuardFunc(func(uint, models.CapabilityTier) error { return revoked })}
	app := &models.Application{WorkspaceID: 3, AddCapabilities: []string{"NET_ADMIN"}}

	if _, err := b.withGrants(Security{}, app); !errors.Is(err, revoked) {
		t.Errorf("err = %v, want the guard's refusal", err)
	}
}

func TestDeployAppliesGrantsWhenStillAuthorised(t *testing.T) {
	var askedFor models.CapabilityTier
	b := &runtimeBuilder{grants: GrantGuardFunc(func(_ uint, tier models.CapabilityTier) error {
		askedFor = tier
		return nil
	})}
	app := &models.Application{
		WorkspaceID:     3,
		AddCapabilities: []string{"NET_ADMIN"},
		Devices:         []string{"/dev/bus/usb/001/002"},
	}

	sec, err := b.withGrants(Security{}, app)
	if err != nil {
		t.Fatalf("withGrants: %v", err)
	}
	if len(sec.CapAdd) != 1 || sec.CapAdd[0] != "NET_ADMIN" {
		t.Errorf("CapAdd = %v, want [NET_ADMIN]", sec.CapAdd)
	}
	if len(sec.Devices) != 1 {
		t.Errorf("Devices = %v, want the one device", sec.Devices)
	}
	// The device is elevated even though the capability is not, so the guard must
	// be asked about the higher of the two.
	if askedFor != models.TierElevated {
		t.Errorf("guard was asked about tier %d, want elevated — the highest decides", askedFor)
	}
}

// The restricted profile forces a non-root container with capabilities dropped.
// Letting an app add them back would make the profile decorative.
func TestRestrictedProfileRefusesGrants(t *testing.T) {
	b := &runtimeBuilder{} // no guard wired: the profile alone must still refuse
	app := &models.Application{WorkspaceID: 3, AddCapabilities: []string{"NET_ADMIN"}}

	if _, err := b.withGrants(Security{Restricted: true}, app); !errors.Is(err, models.ErrCapabilityRestricted) {
		t.Errorf("err = %v, want ErrCapabilityRestricted", err)
	}
}

// An app with no grants must be unaffected — this is every app on the platform.
func TestNoGrantsIsUntouched(t *testing.T) {
	refuse := GrantGuardFunc(func(uint, models.CapabilityTier) error {
		t.Error("the guard was consulted for an app with no grants")
		return errors.New("should not be called")
	})
	b := &runtimeBuilder{grants: refuse}

	sec, err := b.withGrants(Security{Restricted: true}, &models.Application{WorkspaceID: 3})
	if err != nil {
		t.Fatalf("an app with no grants was refused: %v", err)
	}
	if sec.CapAdd != nil || sec.Devices != nil {
		t.Errorf("grants appeared from nowhere: %v %v", sec.CapAdd, sec.Devices)
	}
}

func TestGrantsReachTheRunSpec(t *testing.T) {
	sec := Security{CapAdd: []string{"NET_ADMIN"}, Devices: []string{"/dev/net/tun"}}
	runSpec := &docker.RunSpec{}
	sec.applyTo(runSpec)
	if len(runSpec.CapAdd) != 1 || runSpec.CapAdd[0] != "NET_ADMIN" {
		t.Errorf("CapAdd did not reach the spec: %v", runSpec.CapAdd)
	}
	if len(runSpec.Devices) != 1 || runSpec.Devices[0] != "/dev/net/tun" {
		t.Errorf("Devices did not reach the spec: %v", runSpec.Devices)
	}
}

// The master switch reaches the deploy path too: an app configured while grants
// were enabled must stop receiving them the moment an operator turns them off,
// without waiting for anyone to edit the app.
func TestDeployRefusesGrantsWhenTheSwitchIsOff(t *testing.T) {
	b := &runtimeBuilder{grants: GrantGuardFunc(func(uint, models.CapabilityTier) error {
		return models.ErrGrantsDisabled
	})}
	app := &models.Application{WorkspaceID: 3, AddCapabilities: []string{"NET_ADMIN"}}

	sec, err := b.withGrants(Security{}, app)
	if !errors.Is(err, models.ErrGrantsDisabled) {
		t.Errorf("err = %v, want ErrGrantsDisabled", err)
	}
	if len(sec.CapAdd) != 0 {
		t.Errorf("capabilities were applied anyway: %v", sec.CapAdd)
	}
}
