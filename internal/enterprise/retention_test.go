// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package enterprise

import "testing"

func TestAnalyticsRetentionDays(t *testing.T) {
	cases := []struct {
		name string
		e    Entitlements
		want int
	}{
		{"community defaults to 7", Entitlements{Edition: EditionCommunity}, CommunityAnalyticsRetentionDays},
		{"zero edition treated as community", Entitlements{}, CommunityAnalyticsRetentionDays},
		{"export flag lifts the cap", Entitlements{Edition: EditionCommunity, Flags: map[string]bool{FlagAnalyticsExport: true}}, -1},
		{"paid edition unlimited by default", Entitlements{Edition: EditionEnterprise}, -1},
		{"explicit limit wins", Entitlements{Edition: EditionEnterprise, Limits: map[string]int{LimitAnalyticsRetentionDays: 30}}, 30},
		{"explicit limit wins over export flag", Entitlements{Flags: map[string]bool{FlagAnalyticsExport: true}, Limits: map[string]int{LimitAnalyticsRetentionDays: 14}}, 14},
	}
	for _, tc := range cases {
		if got := tc.e.AnalyticsRetentionDays(); got != tc.want {
			t.Errorf("%s: AnalyticsRetentionDays()=%d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestClampAnalyticsRetention(t *testing.T) {
	cases := []struct{ configured, cap, want int }{
		{90, 7, 7},   // community caps a generous config down
		{3, 7, 3},    // config under the cap is honored
		{0, 7, 7},    // "keep forever" clamps to the cap
		{-1, 7, 7},   // negative config also clamps
		{90, -1, 90}, // unlimited entitlement honors the config as-is
		{0, -1, 0},   // unlimited entitlement keeps "forever"
		{90, 90, 90}, // exactly at the cap
		{91, 90, 90}, // just over the cap
	}
	for _, tc := range cases {
		if got := ClampAnalyticsRetention(tc.configured, tc.cap); got != tc.want {
			t.Errorf("ClampAnalyticsRetention(%d, %d)=%d, want %d", tc.configured, tc.cap, got, tc.want)
		}
	}
}
