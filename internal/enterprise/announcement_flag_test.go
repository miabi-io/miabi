// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

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
