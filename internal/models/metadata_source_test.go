// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// A stack or an imported compose app is still the user's to edit: nothing upstream would
	// overwrite the change, and there is no template upgrade path to protect.
	for _, val := range []string{ManagedByUser, ManagedByStack, ManagedByStackImport, ""} {
		if _, ok := SourceOwnedElsewhere(Metadata{MetaManagedBy: val}); ok {
			t.Errorf("SourceOwnedElsewhere(%q) reported owned; only marketplace and gitops are", val)
		}
	}
	if _, ok := SourceOwnedElsewhere(nil); ok {
		t.Error("nil metadata should not report an owned source")
	}
}
