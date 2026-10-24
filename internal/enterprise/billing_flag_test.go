// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

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
