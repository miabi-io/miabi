// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestDatabaseSizesFrom(t *testing.T) {
	plan := &models.Plan{DatabaseSizes: []uint{3, 1}}
	if got := databaseSizesFrom(plan, nil); !slices.Equal(got, []uint{3, 1}) {
		t.Errorf("plan sizes = %v", got)
	}
	none := []uint{}
	if got := databaseSizesFrom(plan, &models.WorkspaceQuota{DatabaseSizes: &none}); len(got) != 0 {
		t.Errorf("an empty override must replace the plan's list, got %v", got)
	}
	if got := databaseSizesFrom(plan, &models.WorkspaceQuota{}); !slices.Equal(got, []uint{3, 1}) {
		t.Errorf("an override without sizes must inherit the plan, got %v", got)
	}
	if got := databaseSizesFrom(nil, nil); got != nil {
		t.Errorf("no plan = no binding, got %v", got)
	}
}

// Without enforcement or the database_sizes entitlement, no workspace is bound to sizes.
func TestEffectiveDatabaseSizesNeedsEnforcementAndLicense(t *testing.T) {
	if _, enforced := (&Service{enforce: false, ee: allow{}}).EffectiveDatabaseSizes(1); enforced {
		t.Error("sizes bound with plan enforcement off")
	}
	if _, enforced := (&Service{enforce: true}).EffectiveDatabaseSizes(1); enforced {
		t.Error("sizes bound without the database_sizes entitlement")
	}
	if (&Service{}).DatabaseSizesLicensed() || (*Service)(nil).DatabaseSizesLicensed() {
		t.Error("sizes licensed without an entitlement gate")
	}
	if !(&Service{ee: allow{}}).DatabaseSizesLicensed() {
		t.Error("sizes unlicensed with the entitlement, whatever plan enforcement says")
	}
}
