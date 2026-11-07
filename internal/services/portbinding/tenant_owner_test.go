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

package portbinding

import (
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// On a shared node, other containers belong to other tenants or the platform.
func TestTenantOwnerNamesOnlyTheWorkspacesOwnContainers(t *testing.T) {
	conts := []docker.Container{
		{Names: []string{"/mb-app-shop-web"}, Labels: map[string]string{docker.LabelWorkspace: "5"}},
		{Names: []string{"/mb-app-rival-api"}, Labels: map[string]string{docker.LabelWorkspace: "6"}},
		{Names: []string{"/legacy-nginx"}},
	}
	cases := map[string]string{
		"mb-app-shop-web":     "mb-app-shop-web",
		"mb-app-rival-api":    "another workload on the node",
		"legacy-nginx":        "another workload on the node",
		"an approved binding": "another workload on the node",
	}
	for owner, want := range cases {
		if got := tenantOwner(conts, owner, 5); got != want {
			t.Errorf("owner %q: got %q, want %q", owner, got, want)
		}
	}
}
