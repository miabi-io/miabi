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

func TestNodeIsRefusedOnAStack(t *testing.T) {
	for _, doc := range []string{
		"apiVersion: miabi.io/v1\nkind: Stack\nmetadata: {name: shop}\nspec:\n  placement: {node: safari-02}\n",
	} {
		_, err := Parse([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), "placement.node") {
			t.Errorf("err = %v, want a placement.node refusal for:\n%s", err, doc)
		}
	}
}

func TestApplicationNodeBesideANodeConstraintIsRefused(t *testing.T) {
	doc := "apiVersion: miabi.io/v1\nkind: Application\nmetadata: {name: api}\nspec:\n  image: nginx\n  placement:\n    node: safari-02\n    constraints: [\"node.id==abc\"]\n"
	if _, err := Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), "both choose the node") {
		t.Errorf("err = %v, want a refusal", err)
	}
}

func TestJobNodeDiffs(t *testing.T) {
	actual := Resource{Kind: KindJob, Job: &JobSpec{App: "api", Command: []string{"true"}, Placement: &JobPlacementSpec{Node: "safari-02"}}}
	same := Resource{Kind: KindJob, Job: &JobSpec{App: "api", Command: []string{"true"}}}
	if d := diffJob(actual, same); len(d) != 0 {
		t.Errorf("an omitted node must not diff, got %+v", d)
	}
	moved := Resource{Kind: KindJob, Job: &JobSpec{App: "api", Command: []string{"true"}, Placement: &JobPlacementSpec{Node: "safari-03"}}}
	d := diffJob(actual, moved)
	if len(d) != 1 || d[0].Field != "placement.node" {
		t.Errorf("a moved Job must diff only on placement.node (no rerun), got %+v", d)
	}

	cronA := Resource{Kind: KindCronJob, CronJob: &CronJobSpec{App: "api", Schedule: "0 3 * * *", Command: []string{"true"}, Placement: &JobPlacementSpec{Node: "safari-02"}}}
	cronD := Resource{Kind: KindCronJob, CronJob: &CronJobSpec{App: "api", Schedule: "0 3 * * *", Command: []string{"true"}}}
	if d := diffFields(cronA, cronD); len(d) != 0 {
		t.Errorf("an omitted cron node must not diff, got %+v", d)
	}
}

func TestDatabaseNodeDiffs(t *testing.T) {
	pinned := Resource{Kind: KindDatabaseInstance, DatabaseInstance: &DatabaseInstanceSpec{Engine: "postgres", Placement: &PlacementSpec{Node: "safari-02"}}}
	silent := Resource{Kind: KindDatabaseInstance, DatabaseInstance: &DatabaseInstanceSpec{Engine: "postgres"}}
	if d := diffFields(pinned, silent); len(d) != 0 {
		t.Errorf("an omitted instance node must not diff, got %+v", d)
	}
	db := Resource{Kind: KindDatabase, Database: &DatabaseSpec{Engine: "postgres", Placement: &DatabasePlacementSpec{Node: "safari-02"}}}
	moved := Resource{Kind: KindDatabase, Database: &DatabaseSpec{Engine: "postgres", Placement: &DatabasePlacementSpec{Node: "safari-03"}}}
	if d := diffFields(db, moved); len(d) != 1 || d[0].Field != "placement.node" {
		t.Errorf("a moved database must diff on placement.node, got %+v", d)
	}
	doc := "apiVersion: miabi.io/v1\nkind: Database\nmetadata: {name: shopdb}\nspec:\n  engine: postgres\n  placement: {location: safari-01, node: safari-02}\n"
	set, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := set.Get("Database/shopdb"); r.Database.NodeName() != "safari-02" {
		t.Errorf("node not parsed from the mapping form: %+v", r.Database.Placement)
	}
}
