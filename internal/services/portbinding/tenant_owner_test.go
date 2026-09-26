// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package portbinding

import (
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// A tenant learns the name of a container holding a port only when the container is its own: on a shared
// node the rest belong to other tenants or to the platform.
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
