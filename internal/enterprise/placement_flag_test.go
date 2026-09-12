// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package enterprise

import (
	"slices"
	"testing"
)

// Plan placement is sold from the Business tier up, so the flag must be grantable and those tiers must grant it.
func TestPlacementPolicyEntitlement(t *testing.T) {
	if !IsKnownFlag(FlagPlacementPolicy) {
		t.Fatalf("%s is missing from AllFlags, so no license can grant it", FlagPlacementPolicy)
	}
	for _, tier := range []string{TierBusiness, TierEnterprise} {
		p, ok := TierByName(tier)
		if !ok {
			t.Fatalf("tier %q is not defined", tier)
		}
		if !slices.Contains(p.Flags, FlagPlacementPolicy) {
			t.Errorf("tier %q does not grant %s", tier, FlagPlacementPolicy)
		}
	}
	if p, _ := TierByName(TierProfessional); slices.Contains(p.Flags, FlagPlacementPolicy) {
		t.Errorf("tier %q grants %s", TierProfessional, FlagPlacementPolicy)
	}
}
