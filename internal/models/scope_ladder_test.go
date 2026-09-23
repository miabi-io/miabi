// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "testing"

// The ladder is * ⊃ admin ⊃ {write, deploy} ⊃ read, with write and deploy as siblings. A deploy
// key that could also edit, or a write key that could also ship, would make the two scopes
// decorative.
func TestScopeSatisfies(t *testing.T) {
	for _, tc := range []struct {
		granted  []string
		required string
		want     bool
	}{
		{[]string{ScopeAll}, ScopeAdmin, true},
		{[]string{ScopeAll}, ScopeWrite, true},
		{[]string{ScopeAll}, ScopeRead, true},
		{[]string{ScopeAdmin}, ScopeAdmin, true},
		{[]string{ScopeAdmin}, ScopeWrite, true},
		{[]string{ScopeAdmin}, ScopeDeploy, true},
		{[]string{ScopeAdmin}, ScopeRead, true},
		{[]string{ScopeWrite}, ScopeRead, true},
		{[]string{ScopeWrite}, ScopeWrite, true},
		{[]string{ScopeWrite}, ScopeDeploy, false},
		{[]string{ScopeWrite}, ScopeAdmin, false},
		{[]string{ScopeDeploy}, ScopeRead, true},
		{[]string{ScopeDeploy}, ScopeDeploy, true},
		{[]string{ScopeDeploy}, ScopeWrite, false},
		{[]string{ScopeDeploy}, ScopeAdmin, false},
		{[]string{ScopeRead}, ScopeRead, true},
		{[]string{ScopeRead}, ScopeWrite, false},
		{[]string{ScopeRead}, ScopeDeploy, false},
		{[]string{ScopeRead}, ScopeAdmin, false},
		// The CLI's own login-token grant: everything but administration.
		{[]string{ScopeRead, ScopeWrite, ScopeDeploy}, ScopeDeploy, true},
		{[]string{ScopeRead, ScopeWrite, ScopeDeploy}, ScopeAdmin, false},
		// An empty grant is read-only, the same default HasScope applies.
		{nil, ScopeRead, true},
		{nil, ScopeWrite, false},
		{nil, ScopeAdmin, false},
		// A registry scope grants nothing on the general API; a mixed key rides its general one.
		{[]string{ScopeRegistryWrite}, ScopeRead, false},
		{[]string{ScopeRegistryWrite}, ScopeWrite, false},
		{[]string{ScopeRead, ScopeRegistryWrite}, ScopeRead, true},
	} {
		if got := ScopeSatisfies(tc.granted, tc.required); got != tc.want {
			t.Errorf("ScopeSatisfies(%v, %q) = %v, want %v", tc.granted, tc.required, got, tc.want)
		}
	}
}

// An unclassified route passes "" as the requirement; refusing it would break every route the
// classifier has no opinion on.
func TestScopeSatisfies_EmptyRequirement(t *testing.T) {
	if !ScopeSatisfies(nil, "") {
		t.Fatal("an empty requirement must be satisfied by any key")
	}
}
