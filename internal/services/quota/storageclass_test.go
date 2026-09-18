// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import "testing"

func TestStorageClassBindingAllows(t *testing.T) {
	// An empty list binds nothing: every install without plan enforcement behaves this way, and the
	// node's own default decides.
	unbound := StorageClassBinding{}
	if !unbound.Allows("ssd-fast") || !unbound.Allows("default") {
		t.Fatal("an empty binding allows every class")
	}

	bound := StorageClassBinding{Allowed: []string{"bulk", "default"}, Default: "bulk"}
	if !bound.Allows("bulk") || !bound.Allows("default") {
		t.Fatal("a listed class is allowed")
	}
	if bound.Allows("ssd-fast") {
		t.Fatal("a class outside the plan's list is denied")
	}
}
