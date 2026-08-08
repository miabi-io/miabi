// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runner

import (
	"errors"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ErrNoRunner is returned when no connected, eligible runner can take a job
// right now. The caller queues the run in a "waiting for a runner" state rather
// than falling back to building on a hosting node.
var ErrNoRunner = errors.New("no eligible runner available")

// ConnRegistry reports whether a runner currently has a live tunnel. Satisfied
// by runners.Manager; injected (via SetScheduling) to avoid an import cycle.
type ConnRegistry interface {
	Connected(id uint) bool
}

// SetScheduling wires the runtime the scheduler needs: the live-tunnel registry
// and the lease store (for concurrency accounting). Nil-safe.
func (s *Service) SetScheduling(conn ConnRegistry, leases *repositories.RunnerLeaseRepository) {
	s.conn = conn
	s.leases = leases
}

// Job describes what a build/pipeline run needs from a runner: the tenant it
// belongs to and any required labels (arch/gpu/buildkit/…).
type Job struct {
	WorkspaceID    uint
	RequiredLabels []string
}

// SelectRunner picks the least-loaded eligible runner for job, or ErrNoRunner when none can take it right
// now. Eligible means connected, enabled, not cordoned, labels superset of required, in scope (owned by the
// workspace or shared with plan permission), and active leases below declared concurrency.
func (s *Service) SelectRunner(job Job) (*models.Runner, error) {
	// Shared runners are only in scope when the workspace's plan grants the
	// platform-runners capability; ListSchedulable narrows the candidate set to
	// owned (+ shared when allowed) accordingly.
	includeShared := s.quota.Require(job.WorkspaceID, quota.CapPlatformRunners) == nil
	candidates, err := s.repo.ListSchedulable(job.WorkspaceID, includeShared)
	if err != nil {
		return nil, err
	}
	loads, err := s.activeCounts()
	if err != nil {
		return nil, err
	}
	var best *models.Runner
	bestLoad := 0
	for i := range candidates {
		r := &candidates[i]
		if !eligible(r, job.WorkspaceID, job.RequiredLabels, s.connected(r.ID)) {
			continue
		}
		load := loads[r.ID]
		if load >= r.Concurrency { // no spare capacity
			continue
		}
		if best == nil || load < bestLoad || (load == bestLoad && r.ID < best.ID) {
			best, bestLoad = r, load
		}
	}
	if best == nil {
		return nil, ErrNoRunner
	}
	return best, nil
}

// AvailabilityReason explains why SelectRunner couldn't place job, so a waiting
// deploy/pipeline can tell the user what to fix instead of an opaque "waiting…".
// Returns "" when a runner is in fact schedulable (the caller shouldn't be here).
func (s *Service) AvailabilityReason(job Job) string {
	includeShared := s.quota.Require(job.WorkspaceID, quota.CapPlatformRunners) == nil
	candidates, err := s.repo.ListSchedulable(job.WorkspaceID, includeShared)
	if err != nil {
		return "could not check runner availability: " + err.Error()
	}
	if len(candidates) == 0 {
		// A platform (shared) runner may exist but be gated by the workspace plan.
		if !includeShared {
			if shared, _ := s.repo.ListSchedulable(job.WorkspaceID, true); len(shared) > 0 {
				return "a platform (shared) runner exists but this workspace's plan doesn't include platform runners"
			}
		}
		return "no runner is registered for this workspace — add one in Settings → Runners"
	}
	loads, _ := s.activeCounts()
	var enabled, connected, withCapacity, labelMatch int
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
	case withCapacity == 0:
		return "all runners are busy (at their concurrency limit); the build will start when one frees up"
	}
	return ""
}

// Lease records a runner's at-most-once claim on a job (pipeline run or build,
// per kind) with the given deadline, so it counts against the runner's
// concurrency and a dead lease can be requeued. Returns the created lease.
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

// SweepExpiredLeases marks every active lease past now as expired and returns
// them, so the caller can requeue their runs. Belt-and-suspenders for a runner
// that died without releasing its lease.
func (s *Service) SweepExpiredLeases(now time.Time) ([]models.RunnerLease, error) {
	return s.leases.ExpireDue(now)
}

// activeCounts returns per-runner active-lease counts (empty when no lease store
// is wired, so eligibility still works in tests without one).
func (s *Service) activeCounts() (map[uint]int, error) {
	if s.leases == nil {
		return map[uint]int{}, nil
	}
	return s.leases.ActiveCountsByRunner()
}

// connected reports a runner's live-tunnel state, treating a missing registry as
// "connected" so eligibility can be unit-tested without a live manager.
func (s *Service) connected(id uint) bool {
	return s.conn == nil || s.conn.Connected(id)
}

// eligible is the pure matcher: it decides whether a runner may take a job,
// independent of load. Exported behavior is covered by the scheduler tests.
func eligible(r *models.Runner, workspaceID uint, required []string, connected bool) bool {
	if !r.Enabled || r.Cordoned || !connected {
		return false
	}
	if !inScope(r, workspaceID) {
		return false
	}
	return labelsSatisfy(r.Labels, required)
}

// inScope reports whether a runner may serve the workspace: an owned runner must
// belong to it; a shared runner (nil workspace) is in scope for anyone (the
// plan-capability gate is applied earlier, when the candidate set is built).
func inScope(r *models.Runner, workspaceID uint) bool {
	if r.WorkspaceID != nil {
		return *r.WorkspaceID == workspaceID
	}
	return r.Scope == models.ScopeShared
}

// labelsSatisfy reports whether have ⊇ required (every required label is present
// on the runner). No required labels means any runner matches.
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
