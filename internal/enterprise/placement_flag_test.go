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
