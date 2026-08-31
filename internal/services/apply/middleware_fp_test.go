// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import "testing"

// Go randomizes map iteration, so an unstable fingerprint would make every plan
// show a rule change on a middleware nobody touched.
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

// The point of fingerprinting the rendered rule: a rotated secret is invisible in
// every other diffed field, so without this a rotation would converge to nothing.
func TestMiddlewareRuleFPChangesWithASecret(t *testing.T) {
	a := map[string]any{"users": []any{map[string]any{"username": "ops", "password": "old"}}}
	b := map[string]any{"users": []any{map[string]any{"username": "ops", "password": "new"}}}
	if middlewareRuleFP(a) == middlewareRuleFP(b) {
		t.Error("rotating a password did not change the fingerprint")
	}
}

// An empty rule has no fingerprint at all, so a middleware that carries none
// never compares against a stamped side and invents drift.
func TestMiddlewareRuleFPIsEmptyForAnEmptyRule(t *testing.T) {
	if got := middlewareRuleFP(nil); got != "" {
		t.Errorf("nil rule = %q", got)
	}
	if got := middlewareRuleFP(map[string]any{}); got != "" {
		t.Errorf("empty rule = %q", got)
	}
}

// Reordering a list is a real change (a middleware chain's order is behaviour),
// while reordering map keys is not.
func TestMiddlewareRuleFPDistinguishesListOrderFromKeyOrder(t *testing.T) {
	l1 := map[string]any{"paths": []any{"/a", "/b"}}
	l2 := map[string]any{"paths": []any{"/b", "/a"}}
	if middlewareRuleFP(l1) == middlewareRuleFP(l2) {
		t.Error("list order was ignored")
	}
}
