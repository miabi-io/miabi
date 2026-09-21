// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
	"errors"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// An empty list offers the whole pool. Treating it as "deny all" would silently cut every existing
// plan off from the shared runners it has always used.
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

// Without enforcement or the platform_runners entitlement, no workspace is bound to a subset: a
// stored list goes inert rather than cutting a workspace off when a license lapses.
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
