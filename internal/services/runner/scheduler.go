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
	"errors"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Callers queue the run as "waiting for a runner" rather than building on a hosting node.
var ErrNoRunner = errors.New("no eligible runner available")

// Injected via SetScheduling to avoid an import cycle with runners.Manager.
type ConnRegistry interface {
	Connected(id uint) bool
}

func (s *Service) SetScheduling(conn ConnRegistry, leases *repositories.RunnerLeaseRepository) {
	s.conn = conn
	s.leases = leases
}

type Job struct {
	WorkspaceID      uint
	RequiredLabels   []string
	RequiredFeatures []string
	// For capabilities that shipped before runners advertised features; empty means any version.
	MinVersion string
}

func (s *Service) SelectRunner(job Job) (*models.Runner, error) {
	candidates, err := s.schedulable(job.WorkspaceID)
	if err != nil {
		return nil, err
	}
	loads, err := s.activeCounts()
	if err != nil {
		return nil, err
	}
	best := pick(candidates, job, loads, s.connected)
	if best == nil {
		return nil, ErrNoRunner
	}
	return best, nil
}

// Pure, so the policy is covered without a database or a live tunnel.
func pick(candidates []models.Runner, job Job, loads map[uint]int, connected func(uint) bool) *models.Runner {
	var best *models.Runner
	bestLoad := 0
	for i := range candidates {
		r := &candidates[i]
		if !eligible(r, job, connected(r.ID)) {
			continue
		}
		load := loads[r.ID]
		if load >= r.Concurrency { // no spare capacity
			continue
		}
		if outranks(r, load, best, bestLoad) {
			best, bestLoad = r, load
		}
	}
	return best
}

// Own runners beat the shared pool outright (warm cache, private network); then least load, then lowest id.
func outranks(r *models.Runner, load int, best *models.Runner, bestLoad int) bool {
	if best == nil {
		return true
	}
	if owned(r) != owned(best) {
		return owned(r)
	}
	if load != bestLoad {
		return load < bestLoad
	}
	return r.ID < best.ID
}

// owned reports whether the runner belongs to a workspace rather than the platform pool.
func owned(r *models.Runner) bool { return r.WorkspaceID != nil }

// AvailabilityReason explains why no runner could be placed, so a waiting job tells the user what to fix.
func (s *Service) AvailabilityReason(job Job) string {
	includeShared := s.quota.Require(job.WorkspaceID, quota.CapPlatformRunners) == nil
	candidates, err := s.schedulable(job.WorkspaceID)
	if err != nil {
		return "could not check runner availability: " + err.Error()
	}
	if len(candidates) == 0 {
		if shared, _ := s.repo.ListSchedulable(job.WorkspaceID, true); len(shared) > 0 {
			if !includeShared {
				return "a platform (shared) runner exists but this workspace's plan doesn't include platform runners"
			}
			return "the platform (shared) runners this workspace's plan offers are not registered"
		}
		return "no runner is registered for this workspace — add one in Settings → Runners"
	}
	loads, _ := s.activeCounts()
	var enabled, connected, withCapacity, labelMatch, featureMatch, versionMatch int
	for i := range candidates {
		r := &candidates[i]
		if !r.Enabled || r.Cordoned {
			continue
		}
		enabled++
		if !s.connected(r.ID) {
			continue
		}
		connected++
		if !labelsSatisfy(r.Labels, job.RequiredLabels) {
			continue
		}
		labelMatch++
		if !labelsSatisfy(r.Features, job.RequiredFeatures) {
			continue
		}
		featureMatch++
		if !versionAtLeast(r.Version, job.MinVersion) {
			continue
		}
		versionMatch++
		if loads[r.ID] < r.Concurrency {
			withCapacity++
		}
	}
	switch {
	case enabled == 0:
		return "the registered runner(s) are disabled or cordoned"
	case connected == 0:
		return "the registered runner(s) are offline (not connected) — check the runner is running and reachable"
	case labelMatch == 0:
		return "no connected runner matches the job's required labels"
	case featureMatch == 0:
		return "no connected runner supports " + strings.Join(job.RequiredFeatures, ", ") + " — upgrade the runners to a release that does"
	case versionMatch == 0:
		return "no connected runner is at version " + job.MinVersion + " or newer, which this job needs — upgrade the runners"
	case withCapacity == 0:
		return "all runners are busy (at their concurrency limit); the build will start when one frees up"
	}
	return ""
}

// Lease records a runner's at-most-once claim on a job so it counts against concurrency and can be requeued.
func (s *Service) Lease(runnerID uint, kind models.LeaseKind, runID uint, stepID *uint, deadline time.Time) (*models.RunnerLease, error) {
	now := time.Now()
	l := &models.RunnerLease{
		RunnerID:  runnerID,
		Kind:      kind,
		RunID:     runID,
		StepID:    stepID,
		Status:    models.LeaseActive,
		LeasedAt:  now,
		ExpiresAt: deadline,
	}
	if err := s.leases.Create(l); err != nil {
		return nil, err
	}
	return l, nil
}

// ReleaseRun releases a job's active lease when it reaches a terminal state.
func (s *Service) ReleaseRun(kind models.LeaseKind, runID uint) error {
	return s.leases.Release(kind, runID)
}

// SweepExpiredLeases expires overdue leases for requeue, covering runners that died without releasing.
func (s *Service) SweepExpiredLeases(now time.Time) ([]models.RunnerLease, error) {
	return s.leases.ExpireDue(now)
}

func (s *Service) activeCounts() (map[uint]int, error) {
	if s.leases == nil {
		return map[uint]int{}, nil
	}
	return s.leases.ActiveCountsByRunner()
}

// A missing registry counts as connected so eligibility can be unit-tested without a live manager.
func (s *Service) connected(id uint) bool {
	return s.conn == nil || s.conn.Connected(id)
}

func eligible(r *models.Runner, job Job, connected bool) bool {
	if !r.Enabled || r.Cordoned || !connected {
		return false
	}
	if !inScope(r, job.WorkspaceID) {
		return false
	}
	return labelsSatisfy(r.Labels, job.RequiredLabels) && labelsSatisfy(r.Features, job.RequiredFeatures) &&
		versionAtLeast(r.Version, job.MinVersion)
}

// A source build reports "dev" and counts as current; an empty or unparseable version is too old.
func versionAtLeast(have, min string) bool {
	if min == "" || have == "dev" {
		return true
	}
	canon := func(v string) string {
		if v = strings.TrimSpace(v); v != "" && !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		return v
	}
	have = canon(have)
	return semver.IsValid(have) && semver.Compare(have, canon(min)) >= 0
}

// The plan-capability gate for shared runners is applied earlier, when the candidate set is built.
func inScope(r *models.Runner, workspaceID uint) bool {
	if r.WorkspaceID != nil {
		return *r.WorkspaceID == workspaceID
	}
	return r.Scope == models.ScopeShared
}

func labelsSatisfy(have, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]bool, len(have))
	for _, l := range have {
		set[l] = true
	}
	for _, r := range required {
		if !set[r] {
			return false
		}
	}
	return true
}
