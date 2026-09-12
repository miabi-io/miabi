// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package enterprise

import (
	"slices"
	"testing"
)

// Database sizes are sold from the Business tier up, like plan placement, which they sit beside on a plan.
func TestDatabaseSizesEntitlement(t *testing.T) {
	if !IsKnownFlag(FlagDatabaseSizes) {
		t.Fatalf("%s is missing from AllFlags, so no license can grant it", FlagDatabaseSizes)
	}
	for _, tier := range []string{TierBusiness, TierEnterprise} {
		p, ok := TierByName(tier)
		if !ok {
			t.Fatalf("tier %q is not defined", tier)
		}
		if !slices.Contains(p.Flags, FlagDatabaseSizes) {
			t.Errorf("tier %q does not grant %s", tier, FlagDatabaseSizes)
		}
	}
	if p, _ := TierByName(TierProfessional); slices.Contains(p.Flags, FlagDatabaseSizes) {
		t.Errorf("tier %q grants %s", TierProfessional, FlagDatabaseSizes)
	}
}
