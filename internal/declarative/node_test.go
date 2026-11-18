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

package declarative

import (
	"strings"
	"testing"
)

// Unstated, a node never drifts: the live volume always reports one.
func TestOmittedNodeIsNotADiff(t *testing.T) {
	actual := Resource{Kind: KindVolume, Volume: &VolumeSpec{Placement: &PlacementSpec{Location: "safari", Node: "safari-02"}}}
	desired := Resource{Kind: KindVolume, Volume: &VolumeSpec{Placement: &PlacementSpec{Location: "safari"}}}
	if d := diffFields(actual, desired); len(d) != 0 {
		t.Errorf("omitted node diffed: %+v", d)
	}
	desired.Volume.Placement.Node = "safari-01"
	if d := diffFields(actual, desired); len(d) != 1 || d[0].Field != "placement.node" {
		t.Errorf("a stated different node must diff, got %+v", d)
	}
}

func TestNodeIsRefusedWhereItDoesNotApplyYet(t *testing.T) {
	for _, doc := range []string{
		"apiVersion: miabi.io/v1\nkind: Stack\nmetadata: {name: shop}\nspec:\n  placement: {node: safari-02}\n",
		"apiVersion: miabi.io/v1\nkind: DatabaseInstance\nmetadata: {name: pg}\nspec:\n  engine: postgres\n  placement: {node: safari-02}\n",
	} {
		_, err := Parse([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), "placement.node") {
			t.Errorf("err = %v, want a placement.node refusal for:\n%s", err, doc)
		}
	}
}
