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

// Community may hold CommunityStorageClassLimit classes, the seeded built-in one included, and the
// storage_classes entitlement lifts the cap outright rather than raising it by a number.
func TestStorageClassLimitResolution(t *testing.T) {
	cases := []struct {
		name string
		ent  Entitlements
		want int
	}{
		{"community keeps the built-in class plus one", Entitlements{Edition: EditionCommunity}, CommunityStorageClassLimit},
		{"empty edition treated as community", Entitlements{}, CommunityStorageClassLimit},
		{"the entitlement lifts the cap",
			Entitlements{Edition: EditionEnterprise, Flags: map[string]bool{FlagStorageClasses: true}}, -1},
		{"a paid edition without the flag is still unlimited by count",
			Entitlements{Edition: EditionEnterprise}, -1},
		{"community with the flag (tiered license on a CE label)",
			Entitlements{Edition: EditionCommunity, Flags: map[string]bool{FlagStorageClasses: true}}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ent.StorageClassLimit(); got != tc.want {
				t.Errorf("StorageClassLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}

// The Community cap must leave room for a disk of the operator's own beside the seeded class,
// otherwise registering one is impossible and the feature reads as absent.
func TestCommunityStorageClassCapLeavesRoomForOneDisk(t *testing.T) {
	if CommunityStorageClassLimit < 2 {
		t.Errorf("CommunityStorageClassLimit = %d, leaves no room beside the built-in class", CommunityStorageClassLimit)
	}
}

func TestSharedRunnerLimitResolution(t *testing.T) {
	cases := []struct {
		name string
		ent  Entitlements
		want int
	}{
		{"community", Entitlements{Edition: EditionCommunity}, CommunityRunnerLimit},
		{"empty edition treated as community", Entitlements{}, CommunityRunnerLimit},
		{"the entitlement lifts the cap",
			Entitlements{Edition: EditionEnterprise, Flags: map[string]bool{FlagPlatformRunners: true}}, -1},
		{"a paid edition without the flag is still unlimited by count",
			Entitlements{Edition: EditionEnterprise}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ent.SharedRunnerLimit(); got != tc.want {
				t.Errorf("SharedRunnerLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}
