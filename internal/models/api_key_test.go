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

func TestPurpose(t *testing.T) {
	cases := []struct {
		scopes []string
		want   string
	}{
		{nil, KeyPurposeGeneral}, // empty defaults to read (general)
		{[]string{ScopeRead}, KeyPurposeGeneral},
		{[]string{ScopeWrite}, KeyPurposeGeneral},
		{[]string{ScopeAll}, KeyPurposeGeneral},
		{[]string{ScopeRegistryRead}, KeyPurposeRegistry},
		{[]string{ScopeRegistryWrite}, KeyPurposeRegistry},
		{[]string{ScopeRegistryRead, ScopeRegistryWrite}, KeyPurposeRegistry},
		{[]string{ScopeRegistryWrite, ScopeRead}, KeyPurposeGeneral}, // mixed → general access too
		{[]string{ScopeBilling}, KeyPurposeBilling},
		{[]string{ScopeBilling, ScopeAll}, KeyPurposeBilling}, // billing wins: such a key stays confined
	}
	for _, tc := range cases {
		k := &APIKey{Scopes: tc.scopes}
		if got := k.Purpose(); got != tc.want {
			t.Errorf("Purpose(%v) = %q, want %q", tc.scopes, got, tc.want)
		}
	}
}

func TestBillingScopeNotImplied(t *testing.T) {
	for _, g := range [][]string{{ScopeAll}, {ScopeAdmin}, {ScopeWrite}, nil} {
		if ScopeSatisfies(g, ScopeBilling) {
			t.Errorf("%v satisfies the billing scope", g)
		}
	}
	if !ScopeSatisfies([]string{ScopeBilling}, ScopeBilling) {
		t.Error("a billing key does not satisfy the billing scope")
	}
	if ScopeSatisfies([]string{ScopeBilling}, ScopeRead) {
		t.Error("a billing key satisfies read")
	}
	if ValidScopes[ScopeBilling] {
		t.Error("users can create billing keys")
	}
}
