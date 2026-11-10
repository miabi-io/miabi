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
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

func TestImageTagPlan(t *testing.T) {
	live := d.NewResourceSet()
	live.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindApplication, Metadata: d.Meta{Name: "web"},
		Application: &d.ApplicationSpec{Image: "nginx", Tag: "1.25"},
	})
	cases := []struct {
		name, spec string
		updates    int
	}{
		{"separate tag", "  image: nginx\n  tag: \"1.25\"\n", 0},
		{"embedded tag", "  image: nginx:1.25\n", 0},
		{"embedded and same tag", "  image: nginx:1.25\n  tag: \"1.25\"\n", 0},
		{"new separate tag", "  image: nginx\n  tag: \"1.26\"\n", 1},
		{"new embedded tag", "  image: nginx:1.26\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "apiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: web }\nspec:\n" + tc.spec
			desired, err := d.Parse([]byte(src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			plan := d.BuildPlan(desired, live, d.PlanOptions{})
			if _, u, _, _ := plan.Counts(); u != tc.updates {
				t.Fatalf("want %d updates, got %d (%+v)", tc.updates, u, plan.Changes)
			}
		})
	}
}

func TestImageTagConflictRejected(t *testing.T) {
	src := "apiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: web }\nspec:\n  image: registry.example.com:5000/team/nginx:1.25\n  tag: \"1.26\"\n"
	_, err := d.Parse([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "already carries tag") {
		t.Fatalf("want conflicting tag error, got %v", err)
	}
}

func TestRegistryPortIsNotATag(t *testing.T) {
	src := "apiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: web }\nspec:\n  image: registry.example.com:5000/team/nginx\n  tag: \"1.26\"\n"
	if _, err := d.Parse([]byte(src)); err != nil {
		t.Fatalf("a registry port must not count as a tag: %v", err)
	}
}
