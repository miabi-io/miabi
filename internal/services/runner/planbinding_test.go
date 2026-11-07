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

package runner

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
)

func names(rs []models.Runner) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A plan describes the shared pool; the workspace's own runners survive whatever it lists.
func TestOfferedByPlanKeepsOwnedRunners(t *testing.T) {
	ws := uint(1)
	all := []models.Runner{
		{ID: 1, Name: "shared-big", Scope: models.ScopeShared},
		{ID: 2, Name: "shared-gpu", Scope: models.ScopeShared},
		{ID: 3, Name: "mine", WorkspaceID: &ws, Scope: models.ScopeWorkspace},
	}

	got := offeredByPlan(all, quota.RunnerBinding{Allowed: []string{"shared-gpu"}})
	if want := []string{"shared-gpu", "mine"}; !equal(names(got), want) {
		t.Errorf("bound plan = %v, want %v", names(got), want)
	}

	// An empty binding offers the whole pool.
	got = offeredByPlan(all, quota.RunnerBinding{})
	if want := []string{"shared-big", "shared-gpu", "mine"}; !equal(names(got), want) {
		t.Errorf("empty binding = %v, want %v", names(got), want)
	}

	// A plan naming a deleted runner leaves the workspace its own machines rather than nothing.
	got = offeredByPlan(all, quota.RunnerBinding{Allowed: []string{"deleted-runner"}})
	if want := []string{"mine"}; !equal(names(got), want) {
		t.Errorf("stale binding = %v, want %v", names(got), want)
	}
}
