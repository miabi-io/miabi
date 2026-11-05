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

// The Billing API is an add-on: the flag must be grantable, yet no tier preset includes it, Enterprise
// included; a license carries it only when it was sold.
func TestBillingEntitlement(t *testing.T) {
	if !IsKnownFlag(FlagBilling) {
		t.Fatalf("%s is missing from AllFlags, so no license can grant it", FlagBilling)
	}
	if !IsAddOn(FlagBilling) {
		t.Fatalf("%s must be an add-on", FlagBilling)
	}
	ent, ok := TierByName(TierEnterprise)
	if !ok || len(ent.Flags) != len(AllFlags)-len(AddOnFlags) {
		t.Fatalf("the Enterprise tier must grant every flag but the add-ons: %d of %d", len(ent.Flags), len(AllFlags))
	}
	for _, tier := range []string{TierProfessional, TierBusiness, TierEnterprise} {
		if p, ok := TierByName(tier); ok && slices.Contains(p.Flags, FlagBilling) {
			t.Errorf("tier %q grants %s", tier, FlagBilling)
		}
	}
}
