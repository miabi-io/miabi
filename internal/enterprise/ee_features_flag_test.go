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

// ee_features gates capabilities meant for every paid customer, so each tier must grant it.
func TestEEFeaturesEntitlement(t *testing.T) {
	if !IsKnownFlag(FlagEEFeatures) {
		t.Fatalf("%s is missing from AllFlags, so no license can grant it", FlagEEFeatures)
	}
	for _, tier := range Tiers {
		if !slices.Contains(tier.Flags, FlagEEFeatures) {
			t.Errorf("tier %q does not grant %s", tier.Name, FlagEEFeatures)
		}
	}
}
