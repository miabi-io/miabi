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

package pipeline

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestPickRepoOwnedIgnoresManualPipelines(t *testing.T) {
	defs := []models.PipelineDefinition{
		{ID: 3, Name: "hand-written", Source: models.PipelineSourceManual},
		{ID: 5, Name: "from-repo", Source: models.PipelineSourceRepo},
	}
	got := pickRepoOwned(defs)
	if got == nil || got.ID != 5 {
		t.Fatalf("want the repo-owned pipeline, got %+v", got)
	}
}

func TestPickRepoOwnedPrefersOldest(t *testing.T) {
	// A second repo-owned pipeline must not take over the deploy path.
	defs := []models.PipelineDefinition{
		{ID: 9, Source: models.PipelineSourceRepo},
		{ID: 4, Source: models.PipelineSourceRepo},
		{ID: 7, Source: models.PipelineSourceRepo},
	}
	if got := pickRepoOwned(defs); got == nil || got.ID != 4 {
		t.Fatalf("want id 4, got %+v", got)
	}
}

func TestPickRepoOwnedNoneMeansDirectBuild(t *testing.T) {
	// An empty Source (pre-adoption pipelines) must never claim an app's deploys.
	defs := []models.PipelineDefinition{
		{ID: 1, Source: models.PipelineSourceManual},
		{ID: 2, Source: ""},
	}
	if got := pickRepoOwned(defs); got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
	if got := pickRepoOwned(nil); got != nil {
		t.Fatalf("want nil for no pipelines, got %+v", got)
	}
}

func TestIsRepoOwned(t *testing.T) {
	for _, tc := range []struct {
		src  models.PipelineSource
		want bool
	}{
		{models.PipelineSourceRepo, true},
		{models.PipelineSourceManual, false},
		{"", false}, // legacy rows
	} {
		p := &models.PipelineDefinition{Source: tc.src}
		if got := p.IsRepoOwned(); got != tc.want {
			t.Errorf("Source=%q: IsRepoOwned() = %v, want %v", tc.src, got, tc.want)
		}
	}
}

// Edits beyond enabled must be refused, or the next run's re-sync would silently revert them.
func TestRepoOwnedEditRequested(t *testing.T) {
	appID := uint(7)
	other := uint(8)
	stored := &models.PipelineDefinition{
		Name: "guestbook", DisplayName: "Guestbook", Spec: guestbookPipeline, ApplicationID: &appID,
	}
	enabled := true

	for _, tc := range []struct {
		name string
		in   Input
		want bool
	}{
		{"no change", Input{}, false},
		{"enabled only", Input{Enabled: &enabled}, false},
		{"same values round-tripped", Input{
			Name: "guestbook", DisplayName: "Guestbook", Spec: guestbookPipeline,
			SetApplicationID: true, ApplicationID: &appID, Enabled: &enabled,
		}, false},
		{"spec edited", Input{Spec: "apiVersion: miabi.io/v1\nkind: Pipeline\n"}, true},
		{"renamed", Input{Name: "something-else"}, true},
		{"display name changed", Input{DisplayName: "Renamed"}, true},
		{"rebound to another app", Input{SetApplicationID: true, ApplicationID: &other}, true},
		{"unbound", Input{SetApplicationID: true, ApplicationID: nil}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := repoOwnedEditRequested(stored, tc.in); got != tc.want {
				t.Errorf("repoOwnedEditRequested() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSameAppID(t *testing.T) {
	a, b := uint(1), uint(1)
	c := uint(2)
	if !sameAppID(nil, nil) {
		t.Error("nil == nil")
	}
	if sameAppID(&a, nil) || sameAppID(nil, &a) {
		t.Error("nil must differ from a bound app")
	}
	if !sameAppID(&a, &b) {
		t.Error("equal values must compare equal")
	}
	if sameAppID(&a, &c) {
		t.Error("different values must compare unequal")
	}
}

func TestRefLabel(t *testing.T) {
	if got := refLabel(""); got != "the default branch" {
		t.Errorf("refLabel(\"\") = %q", got)
	}
	if got := refLabel("dev"); got != "dev" {
		t.Errorf("refLabel(\"dev\") = %q", got)
	}
}
