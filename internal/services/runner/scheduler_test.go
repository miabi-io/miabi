// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runner

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func ptr(u uint) *uint { return &u }

func TestLabelsSatisfy(t *testing.T) {
	cases := []struct {
		name           string
		have, required []string
		want           bool
	}{
		{"no requirement matches any", []string{"arch=amd64"}, nil, true},
		{"exact subset", []string{"arch=amd64", "buildkit", "gpu"}, []string{"arch=amd64", "gpu"}, true},
		{"missing one fails", []string{"arch=amd64"}, []string{"arch=amd64", "gpu"}, false},
		{"empty runner cannot satisfy a requirement", nil, []string{"buildkit"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := labelsSatisfy(tc.have, tc.required); got != tc.want {
				t.Errorf("labelsSatisfy(%v,%v) = %v, want %v", tc.have, tc.required, got, tc.want)
			}
		})
	}
}

func TestInScope(t *testing.T) {
	owned := &models.Runner{WorkspaceID: ptr(42), Scope: models.ScopeWorkspace}
	shared := &models.Runner{WorkspaceID: nil, Scope: models.ScopeShared}
	if !inScope(owned, 42) || inScope(owned, 7) {
		t.Error("owned runner is in scope only for its own workspace")
	}
	if !inScope(shared, 42) || !inScope(shared, 7) {
		t.Error("shared runner is in scope for any workspace")
	}
}

func TestEligible(t *testing.T) {
	base := func() *models.Runner {
		return &models.Runner{
			WorkspaceID: ptr(1), Scope: models.ScopeWorkspace,
			Enabled: true, Cordoned: false, Labels: []string{"arch=amd64", "buildkit"},
		}
	}
	if !eligible(base(), Job{WorkspaceID: 1, RequiredLabels: []string{"buildkit"}}, true) {
		t.Error("a connected, enabled, in-scope, label-matching runner should be eligible")
	}
	// Each disqualifier independently makes it ineligible.
	r := base()
	r.Enabled = false
	if eligible(r, Job{WorkspaceID: 1, RequiredLabels: nil}, true) {
		t.Error("disabled runner must be ineligible")
	}
	r = base()
	r.Cordoned = true
	if eligible(r, Job{WorkspaceID: 1, RequiredLabels: nil}, true) {
		t.Error("cordoned runner must be ineligible")
	}
	if eligible(base(), Job{WorkspaceID: 1, RequiredLabels: nil}, false) {
		t.Error("disconnected runner must be ineligible")
	}
	if eligible(base(), Job{WorkspaceID: 2, RequiredLabels: nil}, true) {
		t.Error("out-of-scope runner must be ineligible")
	}
	if eligible(base(), Job{WorkspaceID: 1, RequiredLabels: []string{"gpu"}}, true) {
		t.Error("runner missing a required label must be ineligible")
	}
}

func TestSelectionPrefersLeastLoadedWithCapacity(t *testing.T) {
	runners := []models.Runner{
		{ID: 1, WorkspaceID: ptr(1), Scope: models.ScopeWorkspace, Enabled: true, Concurrency: 2, Labels: []string{"buildkit"}},
		{ID: 2, WorkspaceID: ptr(1), Scope: models.ScopeWorkspace, Enabled: true, Concurrency: 2, Labels: []string{"buildkit"}},
		{ID: 3, WorkspaceID: ptr(1), Scope: models.ScopeWorkspace, Enabled: true, Concurrency: 1, Labels: []string{"buildkit"}},
	}
	always := func(uint) bool { return true }

	// #2 has the fewest active leases → chosen. #3 is also at 0, so the lower id breaks the tie.
	loads := map[uint]int{1: 1, 2: 0, 3: 0}
	if got := pick(runners, Job{WorkspaceID: 1, RequiredLabels: []string{"buildkit"}}, loads, always); got == nil || got.ID != 2 {
		t.Fatalf("least-loaded selection = %v, want runner 2", got)
	}

	// Saturate #1 and #2; only #3 has spare capacity (0 < 1).
	full := map[uint]int{1: 2, 2: 2, 3: 0}
	if got := pick(runners, Job{WorkspaceID: 1, RequiredLabels: []string{"buildkit"}}, full, always); got == nil || got.ID != 3 {
		t.Fatalf("with 1&2 saturated, selection = %v, want runner 3", got)
	}

	// Everyone saturated → no runner (caller queues, "waiting for a runner").
	saturated := map[uint]int{1: 2, 2: 2, 3: 1}
	if got := pick(runners, Job{WorkspaceID: 1, RequiredLabels: []string{"buildkit"}}, saturated, always); got != nil {
		t.Fatalf("all saturated: selection = %v, want none", got)
	}

	// A required label no runner has → no match.
	if got := pick(runners, Job{WorkspaceID: 1, RequiredLabels: []string{"gpu"}}, loads, always); got != nil {
		t.Fatalf("unmatched label: selection = %v, want none", got)
	}
}

// A workspace that registered its own runner must get it, even when a shared runner is idle and
// has a lower id. This was decided by primary-key order before: operators register the platform
// pool first, so the shared runner won every tie and the tenant's machine sat idle.
func TestSelectionPrefersTheWorkspacesOwnRunner(t *testing.T) {
	shared := models.Runner{ID: 1, Scope: models.ScopeShared, Enabled: true, Concurrency: 2}
	own := models.Runner{ID: 9, WorkspaceID: ptr(1), Scope: models.ScopeWorkspace, Enabled: true, Concurrency: 2}
	candidates := []models.Runner{shared, own}
	always := func(uint) bool { return true }

	if got := pick(candidates, Job{WorkspaceID: 1}, map[uint]int{}, always); got == nil || got.ID != 9 {
		t.Fatalf("both idle: selection = %v, want the workspace's own runner 9", got)
	}

	// Busier, but still theirs: a build on a warm cache beats relocating to the shared pool.
	if got := pick(candidates, Job{WorkspaceID: 1}, map[uint]int{1: 0, 9: 1}, always); got == nil || got.ID != 9 {
		t.Fatalf("own runner busier: selection = %v, want 9 (own beats shared ahead of load)", got)
	}

	// Saturated, not merely busy → the shared pool takes it rather than the job waiting forever.
	if got := pick(candidates, Job{WorkspaceID: 1}, map[uint]int{1: 0, 9: 2}, always); got == nil || got.ID != 1 {
		t.Fatalf("own runner saturated: selection = %v, want the shared runner 1", got)
	}

	// Offline own runner → the shared pool, so a dead runner does not block every build.
	onlyShared := func(id uint) bool { return id == 1 }
	if got := pick(candidates, Job{WorkspaceID: 1}, map[uint]int{}, onlyShared); got == nil || got.ID != 1 {
		t.Fatalf("own runner offline: selection = %v, want the shared runner 1", got)
	}

	// With no runner of their own, the shared pool is still used.
	if got := pick([]models.Runner{shared}, Job{WorkspaceID: 1}, map[uint]int{}, always); got == nil || got.ID != 1 {
		t.Fatalf("no own runner: selection = %v, want the shared runner 1", got)
	}
}

// Two owned runners rank against each other by load, not by scope.
func TestSelectionRanksWithinTheOwnedTierByLoad(t *testing.T) {
	candidates := []models.Runner{
		{ID: 4, WorkspaceID: ptr(1), Scope: models.ScopeWorkspace, Enabled: true, Concurrency: 3},
		{ID: 5, WorkspaceID: ptr(1), Scope: models.ScopeWorkspace, Enabled: true, Concurrency: 3},
	}
	always := func(uint) bool { return true }
	if got := pick(candidates, Job{WorkspaceID: 1}, map[uint]int{4: 2, 5: 1}, always); got == nil || got.ID != 5 {
		t.Fatalf("selection = %v, want the least-loaded owned runner 5", got)
	}
}

// A job needing a feature goes only to a runner that reports it: an older runner accepts the job and drops
// what it does not understand, building a single-platform image as if nothing had been asked.
func TestFeaturesGateEligibility(t *testing.T) {
	always := func(uint) bool { return true }
	old := models.Runner{ID: 1, Name: "old", Enabled: true, Concurrency: 2, Scope: models.ScopeShared}
	current := models.Runner{ID: 2, Name: "current", Enabled: true, Concurrency: 2, Scope: models.ScopeShared, Features: []string{"multi-platform"}}
	job := Job{WorkspaceID: 1, RequiredFeatures: []string{"multi-platform"}}

	if got := pick([]models.Runner{old, current}, job, map[uint]int{2: 1}, always); got == nil || got.ID != 2 {
		t.Errorf("picked %+v, want the runner that reports the feature, even though it is busier", got)
	}
	if got := pick([]models.Runner{old}, job, map[uint]int{}, always); got != nil {
		t.Errorf("picked %s, which does not report multi-platform", got.Name)
	}
	if got := pick([]models.Runner{old}, Job{WorkspaceID: 1}, map[uint]int{}, always); got == nil {
		t.Error("a job needing no feature must still run on an older runner")
	}
}

// Context and build-args shipped before runners advertised features; an older runner drops them and builds
// the wrong image, so a job using them waits for a runner new enough.
func TestMinVersionGatesEligibility(t *testing.T) {
	always := func(uint) bool { return true }
	runner := func(id uint, version string) models.Runner {
		return models.Runner{ID: id, Name: version, Enabled: true, Concurrency: 1, Scope: models.ScopeShared, Version: version}
	}
	job := Job{WorkspaceID: 1, MinVersion: "0.0.8"}
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"0.0.8", true}, {"v0.1.0", true}, {"0.0.10", true}, {"dev", true},
		{"0.0.7", false}, {"v0.0.8-rc.1", false}, {"", false}, {"garbage", false},
	} {
		if got := pick([]models.Runner{runner(1, tc.version)}, job, map[uint]int{}, always) != nil; got != tc.ok {
			t.Errorf("runner version %q eligible = %v, want %v", tc.version, got, tc.ok)
		}
	}
	if pick([]models.Runner{runner(1, "")}, Job{WorkspaceID: 1}, map[uint]int{}, always) == nil {
		t.Error("a job with no minimum must still run on a runner that reports no version")
	}
}
