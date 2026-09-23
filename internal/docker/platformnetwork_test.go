// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import "testing"

// The labels are the test, never the name: an operator names the private network in their own stack
// manifest, and a renamed one must stay just as closed as the default.
func TestIsPlatformNetwork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"the private network", map[string]string{LabelRole: RolePlatformInternal}, true},
		{"any other platform role", map[string]string{LabelRole: RoleGateway}, true},
		{"part of the platform stack", map[string]string{LabelPartOf: PartOfMiabi}, true},
		{"a tenant network", map[string]string{LabelWorkspace: "7"}, false},
		{"an unlabelled network", nil, false},
		{"an operator's own network", map[string]string{"com.example.env": "prod"}, false},
	} {
		if got := IsPlatformNetwork(tc.labels); got != tc.want {
			t.Errorf("%s: IsPlatformNetwork(%v) = %v, want %v", tc.name, tc.labels, got, tc.want)
		}
	}
}

// A guard that compares the name alone is bypassed by passing the id, which the engine accepts
// everywhere it accepts a name — that was the hole in the node's RunContainer check.
func TestNetworkRefMatches(t *testing.T) {
	n := Network{ID: "9f8e7d6c5b4a3210fedc", Name: "acme_platform_internal"}
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"acme_platform_internal", true},
		{"9f8e7d6c5b4a3210fedc", true},
		{"9f8e7d6c5b4a", true}, // the short id docker prints
		{"9f8e7d6c5b4a3210", true},
		{"", false},
		{"9f8e", false}, // too short to be a docker ref; not a prefix match
		{"other", false},
		{"acme_platform", false},
	} {
		if got := NetworkRefMatches(n, tc.ref); got != tc.want {
			t.Errorf("NetworkRefMatches(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}
