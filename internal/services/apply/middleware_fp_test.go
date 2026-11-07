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

package apply

import "testing"

// Go randomizes map iteration, so an unstable fingerprint would show phantom rule changes.
func TestMiddlewareRuleFPIsStableAcrossMapOrder(t *testing.T) {
	rule := map[string]any{
		"realm": "Admin",
		"users": []any{
			map[string]any{"username": "ops", "password": "s3cret"},
			map[string]any{"username": "dev", "password": "hunter2"},
		},
		"limits": map[string]any{"burst": 5, "unit": "minute"},
	}
	first := middlewareRuleFP(rule)
	if first == "" {
		t.Fatal("no fingerprint")
	}
	for i := 0; i < 50; i++ {
		if got := middlewareRuleFP(rule); got != first {
			t.Fatalf("run %d gave %s, want %s", i, got, first)
		}
	}
}

// A rotated secret is invisible in every other diffed field.
func TestMiddlewareRuleFPChangesWithASecret(t *testing.T) {
	a := map[string]any{"users": []any{map[string]any{"username": "ops", "password": "old"}}}
	b := map[string]any{"users": []any{map[string]any{"username": "ops", "password": "new"}}}
	if middlewareRuleFP(a) == middlewareRuleFP(b) {
		t.Error("rotating a password did not change the fingerprint")
	}
}

func TestMiddlewareRuleFPIsEmptyForAnEmptyRule(t *testing.T) {
	if got := middlewareRuleFP(nil); got != "" {
		t.Errorf("nil rule = %q", got)
	}
	if got := middlewareRuleFP(map[string]any{}); got != "" {
		t.Errorf("empty rule = %q", got)
	}
}

// List order is behaviour (a chain's order); map key order is not.
func TestMiddlewareRuleFPDistinguishesListOrderFromKeyOrder(t *testing.T) {
	l1 := map[string]any{"paths": []any{"/a", "/b"}}
	l2 := map[string]any{"paths": []any{"/b", "/a"}}
	if middlewareRuleFP(l1) == middlewareRuleFP(l2) {
		t.Error("list order was ignored")
	}
}
