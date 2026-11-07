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
	"errors"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Treating empty as "deny all" would cut every existing plan off from its shared runners.
func TestRunnerBindingAllows(t *testing.T) {
	if !(RunnerBinding{}).Allows("anything") {
		t.Error("an empty binding must offer every shared runner")
	}
	b := RunnerBinding{Allowed: []string{"big-amd64", "gpu-box"}}
	if !b.Allows("gpu-box") {
		t.Error("a listed runner must be allowed")
	}
	if b.Allows("someone-elses") {
		t.Error("an unlisted runner must be denied")
	}
}

func TestPlatformRunnersFrom(t *testing.T) {
	plan := &models.Plan{PlatformRunners: []string{"big-amd64"}}
	if got := platformRunnersFrom(plan, nil); !slices.Equal(got, []string{"big-amd64"}) {
		t.Errorf("plan runners = %v", got)
	}
	none := []string{}
	if got := platformRunnersFrom(plan, &models.WorkspaceQuota{PlatformRunners: &none}); len(got) != 0 {
		t.Errorf("an empty override must replace the plan's list, got %v", got)
	}
	if got := platformRunnersFrom(plan, &models.WorkspaceQuota{}); !slices.Equal(got, []string{"big-amd64"}) {
		t.Errorf("an override without runners must inherit the plan, got %v", got)
	}
	if got := platformRunnersFrom(nil, nil); got != nil {
		t.Errorf("no plan = no binding, got %v", got)
	}
}

// A stored list goes inert rather than cutting a workspace off when a license lapses.
func TestEffectivePlatformRunnersNeedsEnforcementAndLicense(t *testing.T) {
	if _, bound := (&Service{enforce: false, ee: allow{}}).EffectivePlatformRunners(1); bound {
		t.Error("runners bound with plan enforcement off")
	}
	if _, bound := (&Service{enforce: true}).EffectivePlatformRunners(1); bound {
		t.Error("runners bound without the platform_runners entitlement")
	}
	if (&Service{}).PlatformRunnersLicensed() || (*Service)(nil).PlatformRunnersLicensed() {
		t.Error("licensed without an entitlement gate")
	}
	if !(&Service{ee: allow{}}).PlatformRunnersLicensed() {
		t.Error("unlicensed with the entitlement, whatever plan enforcement says")
	}
}

// A nil service and an unenforced one must never refuse a runner.
func TestRequirePlatformRunnerIsPermissiveWithoutABinding(t *testing.T) {
	for _, s := range []*Service{nil, {}, {enforce: true}, {enforce: false, ee: allow{}}} {
		if err := s.RequirePlatformRunner(1, "big-amd64"); err != nil {
			t.Errorf("unbound service refused a runner: %v", err)
		}
	}
}

// The refusal carries the sentinel and the stable code the API envelope publishes.
func TestRequirePlatformRunnerDeniedCarriesCode(t *testing.T) {
	err := (&codedError{code: "CAPABILITY_DENIED", msg: "x", base: ErrCapabilityDenied}).Unwrap()
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Fatal("codedError must unwrap to the capability sentinel")
	}
}
