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
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

var (
	ErrInvalidVersion  = errors.New("invalid version")
	ErrInvalidSeries   = errors.New("invalid series")
	ErrInvalidTag      = errors.New("invalid image tag")
	ErrInvalidSettings = errors.New("invalid versioning settings")
	// ErrBumpOutsideSeries rejects an explicit bump the active series forbids, e.g. minor on 1.4.x.
	ErrBumpOutsideSeries = errors.New("this bump would leave the active series — switch the series instead")
	// ErrOutsideSeries rejects an exact version that does not lie on the series.
	ErrOutsideSeries = errors.New("the version is not on the active series — switch the series first")
	// ErrNotGreater rejects an exact version at or below the series head; versions only move forward.
	ErrNotGreater   = errors.New("the version must be greater than the latest version on its series")
	ErrVersionTaken = errors.New("that version was already used")
	// ErrOverrideNotSupported rejects a bump or exact version on a scheme or branch that has none.
	ErrOverrideNotSupported = errors.New("this run cannot take a version override")
)

// DefaultSeries and DefaultStart open a new semver policy at v1.0.0.
const (
	DefaultSeries = "1.x.x"
	DefaultStart  = "1.0.0"
)

// Request is what a run asks of its policy.
type Request struct {
	Branch        string
	Commit        string
	CommitMessage string
	// Bump and Exact are per-run overrides from a manual or API trigger.
	Bump  models.VersionBump
	Exact string
	Now   time.Time
}

// Plan is the version a run would receive. Skip means the policy gives this run no version, and the
// image keeps its run-<number> tag.
type Plan struct {
	Skip       bool               `json:"skip,omitempty"`
	Version    string             `json:"version"`
	Tag        string             `json:"tag"`
	Series     string             `json:"series,omitempty"`
	Branch     string             `json:"branch,omitempty"`
	Prerelease bool               `json:"prerelease"`
	Bump       models.VersionBump `json:"bump,omitempty"`
	Exact      bool               `json:"exact,omitempty"`
	Note       string             `json:"note,omitempty"`
}

// Next works out the version a run receives, given every live allocation of the policy.
func Next(p *models.VersionPolicy, existing []models.VersionAllocation, req Request) (Plan, error) {
	release := isReleaseBranch(p, req.Branch)
	if !release && p.OtherBranches == models.OtherBranchesRunTag {
		return Plan{Skip: true}, nil
	}
	switch p.Scheme {
	case models.VersionSchemeSemver:
		return nextSemver(p, existing, req, release)
	case models.VersionSchemeCalver:
		return nextCalver(p, existing, req, release)
	}
	if req.Bump != "" || req.Exact != "" {
		return Plan{}, ErrOverrideNotSupported
	}
	return Plan{Skip: true}, nil
}

func isReleaseBranch(p *models.VersionPolicy, branch string) bool {
	if branch == "" || len(p.ReleaseBranches) == 0 {
		return true
	}
	return branchMatches(p.ReleaseBranches, branch) || branchMatches(sortedKeys(p.BranchSeries), branch)
}

// seriesFor picks the series a branch releases on: the first matching branch mapping, else the policy's.
func seriesFor(p *models.VersionPolicy, branch string) (Series, error) {
	for _, pattern := range sortedKeys(p.BranchSeries) {
		if branch != "" && branchMatches([]string{pattern}, branch) {
			return ParseSeries(p.BranchSeries[pattern])
		}
	}
	s := p.Series
	if s == "" {
		s = DefaultSeries
	}
	return ParseSeries(s)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// seriesHead is the highest release version on the series, or nil when it has none.
func seriesHead(s Series, existing []models.VersionAllocation) *Version {
	var head *Version
	for i := range existing {
		if existing[i].Prerelease {
			continue
		}
		v, err := ParseVersion(existing[i].Version)
		if err != nil || v.Pre != "" || !s.Contains(v) {
			continue
		}
		if head == nil || v.Compare(*head) > 0 {
			head = &v
		}
	}
	return head
}

func nextSemver(p *models.VersionPolicy, existing []models.VersionAllocation, req Request, release bool) (Plan, error) {
	series, err := seriesFor(p, req.Branch)
	if err != nil {
		return Plan{}, err
	}
	if !release && (req.Exact != "" || req.Bump != "") {
		return Plan{}, fmt.Errorf("%w: %q is not a release branch", ErrOverrideNotSupported, req.Branch)
	}
	head := seriesHead(series, existing)
	plan := Plan{Series: series.String(), Branch: req.Branch}

	var next Version
	switch {
	case req.Exact != "":
		v, err := ParseVersion(req.Exact)
		if err != nil {
			return Plan{}, err
		}
		if v.Pre != "" {
			return Plan{}, fmt.Errorf("%w: an exact version cannot be a prerelease", ErrInvalidVersion)
		}
		if !series.Contains(v) {
			return Plan{}, fmt.Errorf("%w (%s is not on %s)", ErrOutsideSeries, v, series)
		}
		if head != nil && v.Compare(*head) <= 0 {
			return Plan{}, fmt.Errorf("%w (%s ≤ %s)", ErrNotGreater, v, head)
		}
		if used(existing, v.String()) {
			return Plan{}, fmt.Errorf("%w: %s", ErrVersionTaken, v)
		}
		next, plan.Exact = v, true
	case head == nil:
		next = series.First(p.Start)
		plan.Bump = p.Bump
	default:
		bump, note, err := chooseBump(p, series, req)
		if err != nil {
			return Plan{}, err
		}
		next, plan.Bump, plan.Note = head.Bump(bump), bump, note
	}

	if !release {
		next.Pre = prereleaseID(next, req.Branch, existing)
		plan.Prerelease = true
	}
	tmpl := p.Template
	if tmpl == "" {
		tmpl = DefaultTemplate
	}
	tag, err := RenderTag(tmpl, semverVars(next, req.Commit))
	if err != nil {
		return Plan{}, err
	}
	plan.Version, plan.Tag = next.String(), tag
	return plan, nil
}

// chooseBump resolves the bump for a run: an explicit override must fit the series, while a default or
// commit-derived bump is lowered to fit, so a push never fails because of the commit's wording.
func chooseBump(p *models.VersionPolicy, series Series, req Request) (models.VersionBump, string, error) {
	if req.Bump != "" {
		if !ValidBump(req.Bump) {
			return "", "", fmt.Errorf("%w: unknown bump %q", ErrInvalidSettings, req.Bump)
		}
		if !series.Allows(req.Bump) {
			return "", "", fmt.Errorf("%w (%s on %s)", ErrBumpOutsideSeries, req.Bump, series)
		}
		return req.Bump, "", nil
	}
	want, source := p.Bump, "policy"
	if !ValidBump(want) {
		want = models.BumpPatch
	}
	if p.BumpFromCommits {
		if b, ok := BumpFromCommit(req.CommitMessage); ok {
			want, source = b, "commit"
		}
	}
	got := series.Clamp(want)
	if got != want {
		return got, fmt.Sprintf("%s bump %s lowered to %s to stay on %s", source, want, got, series), nil
	}
	return got, "", nil
}

func used(existing []models.VersionAllocation, version string) bool {
	for i := range existing {
		if existing[i].Version == version {
			return true
		}
	}
	return false
}

// prereleaseID numbers a branch's builds of one upcoming version: 1.4.3-feat-login.1, .2, …
func prereleaseID(base Version, branch string, existing []models.VersionAllocation) string {
	id := prereleaseBranch(branch)
	prefix := base.Core().String() + "-" + id + "."
	n := 0
	for i := range existing {
		if rest, ok := strings.CutPrefix(existing[i].Version, prefix); ok {
			if k, err := strconv.Atoi(rest); err == nil && k > n {
				n = k
			}
		}
	}
	return id + "." + strconv.Itoa(n+1)
}

func nextCalver(p *models.VersionPolicy, existing []models.VersionAllocation, req Request, release bool) (Plan, error) {
	if req.Bump != "" || req.Exact != "" {
		return Plan{}, fmt.Errorf("%w: calver versions follow the date", ErrOverrideNotSupported)
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	prefix, err := calverPrefix(p.CalverFormat, now.UTC())
	if err != nil {
		return Plan{}, err
	}
	n := 0
	for i := range existing {
		if existing[i].Prerelease {
			continue
		}
		if rest, ok := strings.CutPrefix(existing[i].Version, prefix+"."); ok {
			if k, err := strconv.Atoi(rest); err == nil && k > n {
				n = k
			}
		}
	}
	version := prefix + "." + strconv.Itoa(n+1)
	plan := Plan{Branch: req.Branch}
	if !release {
		id := prereleaseBranch(req.Branch)
		k := 0
		for i := range existing {
			if rest, ok := strings.CutPrefix(existing[i].Version, version+"-"+id+"."); ok {
				if v, err := strconv.Atoi(rest); err == nil && v > k {
					k = v
				}
			}
		}
		version += "-" + id + "." + strconv.Itoa(k+1)
		plan.Prerelease = true
	}
	tmpl := p.Template
	if tmpl == "" {
		tmpl = DefaultTemplate
	}
	tag, err := RenderTag(tmpl, tagVars{version: version, sha: shortSHA(req.Commit)})
	if err != nil {
		return Plan{}, err
	}
	plan.Version, plan.Tag = version, tag
	return plan, nil
}

// groupKey names the sequence an allocation belongs to. Versions within a group are handed out in
// increasing order, so the newest row of a group is its head.
func groupKey(a *models.VersionAllocation) string {
	if a.Prerelease {
		core, _, _ := strings.Cut(a.Version, "-")
		return "pre|" + core + "|" + a.Branch
	}
	if a.Series != "" {
		return "series|" + a.Series
	}
	i := strings.LastIndex(a.Version, ".")
	if i < 0 {
		return "cal|" + a.Version
	}
	return "cal|" + a.Version[:i]
}

// IsHead reports whether no newer allocation exists in a's group, so releasing a leaves no gap.
func IsHead(a *models.VersionAllocation, existing []models.VersionAllocation) bool {
	key := groupKey(a)
	for i := range existing {
		o := &existing[i]
		if o.ID != a.ID && o.ID > a.ID && groupKey(o) == key {
			return false
		}
	}
	return true
}

// Preview lists the next count versions a release branch would receive with the policy's defaults.
func Preview(p *models.VersionPolicy, existing []models.VersionAllocation, branch string, count int, now time.Time) ([]Plan, error) {
	sim := append([]models.VersionAllocation(nil), existing...)
	var out []Plan
	for i := 0; i < count; i++ {
		plan, err := Next(p, sim, Request{Branch: branch, Now: now})
		if err != nil {
			return nil, err
		}
		if plan.Skip {
			return out, nil
		}
		out = append(out, plan)
		sim = append(sim, models.VersionAllocation{Version: plan.Version, Series: plan.Series, Branch: plan.Branch, Prerelease: plan.Prerelease})
	}
	return out, nil
}
