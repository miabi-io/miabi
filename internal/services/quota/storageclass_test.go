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

package quota

import "testing"

func TestStorageClassBindingAllows(t *testing.T) {
	// An empty list binds nothing; the node's own default decides.
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
