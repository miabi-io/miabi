// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package enterprise

import (
	"slices"
	"testing"
)

// Storage classes and recovery points are sold from the lowest paid tier up: they are what a
// bare-metal or dedicated-host operator buys a license for.
func TestStorageAndRecoveryEntitlements(t *testing.T) {
	for _, flag := range []string{FlagStorageClasses, FlagRecoveryPoints} {
		if !IsKnownFlag(flag) {
			t.Fatalf("%s is missing from AllFlags, so no license can grant it", flag)
		}
		for _, tier := range []string{TierProfessional, TierBusiness, TierEnterprise} {
			p, ok := TierByName(tier)
			if !ok {
				t.Fatalf("tier %q is not defined", tier)
			}
			if !slices.Contains(p.Flags, flag) {
				t.Errorf("tier %q does not grant %s", tier, flag)
			}
		}
	}
}

// Community is capped at CommunityNodeLimit nodes, and every paid tier must lift that cap rather
// than lower it: a license that shrank the fleet would be a downgrade.
func TestPaidTiersLiftTheCommunityNodeCap(t *testing.T) {
	for _, tier := range []string{TierProfessional, TierBusiness, TierEnterprise} {
		p, ok := TierByName(tier)
		if !ok {
			t.Fatalf("tier %q is not defined", tier)
		}
		n := p.Limits[LimitNodeLimit]
		if n >= 0 && n <= CommunityNodeLimit {
			t.Errorf("tier %q caps nodes at %d, at or below the Community cap of %d", tier, n, CommunityNodeLimit)
		}
	}
}
