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

package docker

import "testing"

// Test the labels, never the name: an operator may rename the private network.
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

// A name-only guard is bypassed by passing the id; that was the hole in the node's RunContainer check.
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
