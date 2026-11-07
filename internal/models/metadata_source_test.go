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

package models

import "testing"

func TestSourceOwnedElsewhere(t *testing.T) {
	owned := map[string]string{
		"marketplace": ManagedByMarketplace,
		"gitops":      ManagedByGitOps,
	}
	for name, val := range owned {
		t.Run(name+" is owned", func(t *testing.T) {
			owner, ok := SourceOwnedElsewhere(Metadata{MetaManagedBy: val})
			if !ok || owner != val {
				t.Fatalf("SourceOwnedElsewhere(%q) = (%q, %v), want (%q, true)", val, owner, ok, val)
			}
		})
	}
	// Stacks and compose imports stay user-editable: nothing upstream overwrites them and no template upgrade applies.
	for _, val := range []string{ManagedByUser, ManagedByStack, ManagedByStackImport, ""} {
		if _, ok := SourceOwnedElsewhere(Metadata{MetaManagedBy: val}); ok {
			t.Errorf("SourceOwnedElsewhere(%q) reported owned; only marketplace and gitops are", val)
		}
	}
	if _, ok := SourceOwnedElsewhere(nil); ok {
		t.Error("nil metadata should not report an owned source")
	}
}
