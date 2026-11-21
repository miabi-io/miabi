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

package declarative_test

import (
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

func appReconcile(block string) string {
	return "apiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: web }\nspec:\n  image: nginx\n" + block + "\n"
}

func TestReconcileSpec(t *testing.T) {
	if _, err := d.Parse([]byte(appReconcile("  reconcile: { policy: sometimes }"))); err == nil {
		t.Error("an unknown reconcile.policy was accepted")
	}
	parse := func(block string) *d.ResourceSet {
		t.Helper()
		set, err := d.Parse([]byte(appReconcile(block)))
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	live := parse("  reconcile: { policy: enforce, restartExternalStops: true }")
	if d.BuildPlan(parse(""), live, d.PlanOptions{}).HasChanges() {
		t.Error("a manifest without reconcile planned to change the console's choice")
	}
	if d.BuildPlan(parse("  reconcile: { restartExternalStops: true }"), live, d.PlanOptions{}).HasChanges() {
		t.Error("an unstated policy was compared")
	}
	if !d.BuildPlan(parse("  reconcile: { restartExternalStops: false }"), live, d.PlanOptions{}).HasChanges() {
		t.Error("turning restartExternalStops off planned nothing")
	}
}
