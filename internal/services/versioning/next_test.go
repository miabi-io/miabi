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

package versioning

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func policy(t *testing.T, s Settings) *models.VersionPolicy {
	t.Helper()
	n, err := s.Normalize()
	if err != nil {
		t.Fatalf("normalize %+v: %v", s, err)
	}
	p := &models.VersionPolicy{}
	n.apply(p)
	return p
}

func allocs(series string, versions ...string) []models.VersionAllocation {
	out := make([]models.VersionAllocation, len(versions))
	for i, v := range versions {
		out[i] = models.VersionAllocation{ID: uint(i + 1), Version: v, Series: series}
	}
	return out
}

func TestNextSemver(t *testing.T) {
	cases := []struct {
		name     string
		settings Settings
		existing []models.VersionAllocation
		req      Request
		want     string
		tag      string
		err      error
	}{
		{name: "first build opens the series", settings: DefaultSettings(), want: "1.0.0", tag: "v1.0.0"},
		{name: "patch by default", settings: DefaultSettings(), existing: allocs("1.x.x", "1.0.0", "1.0.1"), want: "1.0.2"},
		{name: "head is the highest, not the newest", settings: DefaultSettings(), existing: allocs("1.x.x", "1.3.0", "1.2.9"), want: "1.3.1"},
		{name: "minor override resets patch", settings: DefaultSettings(), existing: allocs("1.x.x", "1.4.7"), req: Request{Bump: models.BumpMinor}, want: "1.5.0"},
		{name: "major override outside the series is refused", settings: DefaultSettings(), existing: allocs("1.x.x", "1.4.7"), req: Request{Bump: models.BumpMajor}, err: ErrBumpOutsideSeries},
		{name: "minor on a patch-only series is refused", settings: Settings{Series: "1.4.x"}, existing: allocs("1.4.x", "1.4.2"), req: Request{Bump: models.BumpMinor}, err: ErrBumpOutsideSeries},
		{name: "switching to 2.x.x starts at 2.0.0", settings: Settings{Series: "2.x.x"}, existing: allocs("1.x.x", "1.9.3"), want: "2.0.0"},
		{name: "switching back continues the old line", settings: Settings{Series: "1.x.x"}, existing: append(allocs("1.x.x", "1.9.3"), models.VersionAllocation{ID: 9, Version: "2.0.0", Series: "2.x.x"}), want: "1.9.4"},
		{name: "start applies to an empty series", settings: Settings{Series: "x.x.x", Start: "0.1.0"}, want: "0.1.0"},
		{name: "exact version", settings: DefaultSettings(), existing: allocs("1.x.x", "1.2.0"), req: Request{Exact: "1.5.0"}, want: "1.5.0"},
		{name: "exact version must move forward", settings: DefaultSettings(), existing: allocs("1.x.x", "1.2.0"), req: Request{Exact: "1.1.0"}, err: ErrNotGreater},
		{name: "exact version must be on the series", settings: DefaultSettings(), req: Request{Exact: "2.0.0"}, err: ErrOutsideSeries},
		{name: "custom template", settings: Settings{Template: "release-{version}"}, want: "1.0.0", tag: "release-1.0.0"},
		{name: "commit bump", settings: Settings{BumpFromCommits: true}, existing: allocs("1.x.x", "1.2.3"), req: Request{CommitMessage: "feat(api): add search"}, want: "1.3.0"},
		{name: "breaking commit is clamped to the series", settings: Settings{BumpFromCommits: true}, existing: allocs("1.x.x", "1.2.3"), req: Request{CommitMessage: "feat!: drop v0 routes"}, want: "1.3.0"},
		{name: "unconventional commit uses the policy bump", settings: Settings{BumpFromCommits: true, Bump: models.BumpMinor}, existing: allocs("1.x.x", "1.2.3"), req: Request{CommitMessage: "wip"}, want: "1.3.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Next(policy(t, tc.settings), tc.existing, tc.req)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if got.Version != tc.want {
				t.Errorf("version = %q, want %q", got.Version, tc.want)
			}
			if tc.tag != "" && got.Tag != tc.tag {
				t.Errorf("tag = %q, want %q", got.Tag, tc.tag)
			}
		})
	}
}

func TestNextBranches(t *testing.T) {
	p := policy(t, Settings{Branches: BranchSettings{Release: []string{"main"}, Series: map[string]string{"release/1.*": "1.x.x"}}, Series: "2.x.x"})
	existing := []models.VersionAllocation{
		{ID: 1, Version: "1.8.0", Series: "1.x.x"},
		{ID: 2, Version: "2.3.1", Series: "2.x.x"},
		{ID: 3, Version: "2.3.2-feat-login.1", Prerelease: true, Branch: "feat/login"},
	}

	main, err := Next(p, existing, Request{Branch: "main"})
	if err != nil || main.Version != "2.3.2" || main.Prerelease {
		t.Fatalf("main = %+v, %v; want 2.3.2", main, err)
	}
	hotfix, err := Next(p, existing, Request{Branch: "release/1.8"})
	if err != nil || hotfix.Version != "1.8.1" || hotfix.Series != "1.x.x" {
		t.Fatalf("release/1.8 = %+v, %v; want 1.8.1 on 1.x.x", hotfix, err)
	}
	feat, err := Next(p, existing, Request{Branch: "feat/login"})
	if err != nil || feat.Version != "2.3.2-feat-login.2" || feat.Tag != "v2.3.2-feat-login.2" {
		t.Fatalf("feat/login = %+v, %v; want 2.3.2-feat-login.2", feat, err)
	}
	if _, err := Next(p, existing, Request{Branch: "feat/login", Bump: models.BumpMinor}); !errors.Is(err, ErrOverrideNotSupported) {
		t.Fatalf("override on a feature branch: err = %v", err)
	}

	p.OtherBranches = models.OtherBranchesRunTag
	if skip, _ := Next(p, existing, Request{Branch: "feat/login"}); !skip.Skip {
		t.Fatalf("run-tag branches should skip versioning, got %+v", skip)
	}
}

func TestNextCalver(t *testing.T) {
	p := policy(t, Settings{Scheme: models.VersionSchemeCalver})
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	got, err := Next(p, []models.VersionAllocation{{ID: 1, Version: "2026.10.1"}, {ID: 2, Version: "2026.9.7"}}, Request{Now: now})
	if err != nil || got.Version != "2026.10.2" || got.Tag != "v2026.10.2" {
		t.Fatalf("calver = %+v, %v; want 2026.10.2", got, err)
	}
	got, _ = Next(p, nil, Request{Now: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)})
	if got.Version != "2026.11.1" {
		t.Fatalf("new month = %q, want 2026.11.1", got.Version)
	}
	if _, err := Next(p, nil, Request{Bump: models.BumpMinor}); !errors.Is(err, ErrOverrideNotSupported) {
		t.Fatalf("calver bump override: err = %v", err)
	}
}

func TestRunSchemeSkips(t *testing.T) {
	got, err := Next(policy(t, Settings{Scheme: models.VersionSchemeRun}), nil, Request{})
	if err != nil || !got.Skip {
		t.Fatalf("run scheme = %+v, %v; want skip", got, err)
	}
}

func TestIsHead(t *testing.T) {
	existing := allocs("1.x.x", "1.0.0", "1.0.1")
	if IsHead(&existing[0], existing) {
		t.Error("1.0.0 has a newer allocation; releasing it would leave a gap")
	}
	if !IsHead(&existing[1], existing) {
		t.Error("1.0.1 is the head")
	}
	pre := models.VersionAllocation{ID: 3, Version: "1.0.2-feat.1", Prerelease: true, Branch: "feat"}
	if !IsHead(&existing[1], append(existing, pre)) {
		t.Error("a prerelease does not supersede a release")
	}
}

func TestPreview(t *testing.T) {
	plans, err := Preview(policy(t, DefaultSettings()), allocs("1.x.x", "1.0.4"), "main", 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range plans {
		got = append(got, p.Version)
	}
	if len(got) != 3 || got[0] != "1.0.5" || got[2] != "1.0.7" {
		t.Fatalf("preview = %v", got)
	}
}

func TestNormalizeRejects(t *testing.T) {
	for name, s := range map[string]Settings{
		"bad series":            {Series: "1.x.4"},
		"fully fixed series":    {Series: "1.2.3"},
		"template without slot": {Template: "latest"},
		"illegal tag":           {Template: "v {version}"},
		"major on 1.x.x":        {Bump: models.BumpMajor},
		"start off series":      {Series: "2.x.x", Start: "1.0.0"},
		"floating version":      {FloatingTags: []string{"v{version}"}},
		"unknown scheme":        {Scheme: "build-number"},
		"unknown calver":        {Scheme: models.VersionSchemeCalver, CalverFormat: "DD.MM"},
	} {
		if _, err := s.Normalize(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestBumpFromCommit(t *testing.T) {
	for msg, want := range map[string]models.VersionBump{
		"fix: crash on empty body":                    models.BumpPatch,
		"feat(ui): dark mode":                         models.BumpMinor,
		"refactor!: new config format":                models.BumpMajor,
		"feat: x\n\nBREAKING CHANGE: removes /v0 api": models.BumpMajor,
	} {
		if got, ok := BumpFromCommit(msg); !ok || got != want {
			t.Errorf("%q = %q, want %q", msg, got, want)
		}
	}
	if _, ok := BumpFromCommit("Merge branch 'main'"); ok {
		t.Error("a merge subject is not a conventional commit")
	}
}

func TestVersionCompare(t *testing.T) {
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0", "1.0.1", "1.10.0"}
	for i := 1; i < len(order); i++ {
		a, _ := ParseVersion(order[i-1])
		b, _ := ParseVersion(order[i])
		if a.Compare(b) >= 0 {
			t.Errorf("%s should sort before %s", a, b)
		}
	}
}
