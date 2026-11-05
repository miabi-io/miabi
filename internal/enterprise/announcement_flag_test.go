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

// Announcements are a paid capability, so the flag must be grantable and the tier
// presets must actually grant it — otherwise the admin UI stays locked for
// customers whose licence is supposed to include it.
func TestAnnouncementsEntitlement(t *testing.T) {
	if !IsKnownFlag(FlagAnnouncements) {
		t.Fatalf("%s is missing from AllFlags, so no license can grant it", FlagAnnouncements)
	}
	for _, tier := range []string{TierBusiness, TierEnterprise} {
		p, ok := TierByName(tier)
		if !ok {
			t.Fatalf("tier %q is not defined", tier)
		}
		if !slices.Contains(p.Flags, FlagAnnouncements) {
			t.Errorf("tier %q does not grant %s", tier, FlagAnnouncements)
		}
	}
}
