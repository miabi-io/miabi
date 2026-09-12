// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestPlacementFrom(t *testing.T) {
	plan := &models.Plan{Placement: models.PlanPlacement{Locations: []uint{2, 1}, Pool: "pro"}}

	if got := placementFrom(plan, nil); got.Pool != "pro" || !slices.Equal(got.Locations, []uint{2, 1}) {
		t.Errorf("plan placement = %+v", got)
	}
	override := &models.WorkspaceQuota{Placement: &models.PlanPlacement{Pool: "gpu"}}
	if got := placementFrom(plan, override); got.Pool != "gpu" || len(got.Locations) != 0 {
		t.Errorf("an override must replace the plan's placement whole, got %+v", got)
	}
	if got := placementFrom(plan, &models.WorkspaceQuota{}); got.Pool != "pro" {
		t.Errorf("an override without placement must inherit the plan, got %+v", got)
	}
	if got := placementFrom(nil, nil); got.Pool != "" || len(got.Locations) != 0 {
		t.Errorf("no plan = no binding, got %+v", got)
	}
}

// Without enforcement or the placement_policy entitlement, no workspace is bound to locations or pools.
func TestEffectivePlacementNeedsEnforcementAndLicense(t *testing.T) {
	if _, enforced := (&Service{enforce: false, ee: allow{}}).EffectivePlacement(1); enforced {
		t.Error("placement applied with plan enforcement off")
	}
	if _, enforced := (&Service{enforce: true}).EffectivePlacement(1); enforced {
		t.Error("placement applied without the placement_policy entitlement")
	}
}

type allow struct{}

func (allow) Has(string) bool { return true }
