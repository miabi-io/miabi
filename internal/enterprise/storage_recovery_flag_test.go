// Copyright (c) 2026 Jonas Kaninda. All rights reserved.
//
// This file is part of Miabi Enterprise Edition.
//
// This file is licensed under the Miabi Enterprise License, not the GNU Affero
// General Public License. You may use it only with a valid Miabi Enterprise
// subscription and under the terms in internal/enterprise/LICENSE.md.
//
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

package enterprise

import (
	"slices"
	"testing"
)

func TestRecoveryPointsEntitlement(t *testing.T) {
	if !IsKnownFlag(FlagRecoveryPoints) {
		t.Fatalf("%s is missing from AllFlags, so no license can grant it", FlagRecoveryPoints)
	}
	for _, tier := range []string{TierProfessional, TierBusiness, TierEnterprise} {
		p, ok := TierByName(tier)
		if !ok {
			t.Fatalf("tier %q is not defined", tier)
		}
		if !slices.Contains(p.Flags, FlagRecoveryPoints) {
			t.Errorf("tier %q does not grant %s", tier, FlagRecoveryPoints)
		}
	}
}

// Dropping a retired flag from AllFlags would shift the order signed into every Enterprise token.
func TestRetiredStorageClassesFlagStaysInCatalog(t *testing.T) {
	if !IsKnownFlag(FlagStorageClasses) {
		t.Fatalf("%s was removed from AllFlags", FlagStorageClasses)
	}
	for _, tier := range []string{TierProfessional, TierBusiness} {
		p, _ := TierByName(tier)
		if slices.Contains(p.Flags, FlagStorageClasses) {
			t.Errorf("tier %q still grants the retired %s", tier, FlagStorageClasses)
		}
	}
}

func TestPlatformRunnersEntitlement(t *testing.T) {
	if !IsKnownFlag(FlagPlatformRunners) {
		t.Fatalf("%s is missing from AllFlags, so no license can grant it", FlagPlatformRunners)
	}
	for _, tier := range []string{TierBusiness, TierEnterprise} {
		p, ok := TierByName(tier)
		if !ok {
			t.Fatalf("tier %q is not defined", tier)
		}
		if !slices.Contains(p.Flags, FlagPlatformRunners) {
			t.Errorf("tier %q does not grant %s", tier, FlagPlatformRunners)
		}
	}
}
