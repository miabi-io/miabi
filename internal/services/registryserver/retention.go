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

package registryserver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/metrics"
	"github.com/miabi-io/miabi/internal/models"
)

const (
	// A real run deletes at most this many batches per repository, so one huge repository can't run all night.
	retentionBatchesPerRepo = 20
	// A run row left "running" this long belongs to a process that died; a new run may start.
	retentionStaleRun = 2 * time.Hour
	// The report keeps this many tag names per repository; counts stay exact.
	retentionTagsInReport = 50
)

var ErrRetentionRunning = errors.New("registry: a retention run is already in progress")

// RetentionPolicy is the platform policy with each workspace's override applied by the engine.
type RetentionPolicy struct {
	KeepImages    int
	MinAgeDays    int
	ReleaseWindow int
	ProtectedTags []string
	Overrides     map[uint]models.RegistryRetentionOverride
}

func PolicyFrom(p *models.RegistryRetention, overrides map[uint]models.RegistryRetentionOverride) RetentionPolicy {
	return RetentionPolicy{
		KeepImages: p.KeepImages, MinAgeDays: p.MinAgeDays, ReleaseWindow: p.ReleaseWindow,
		ProtectedTags: p.ProtectedTags, Overrides: overrides,
	}
}

type RetentionRepoReport struct {
	Repository string   `json:"repository"`
	Tags       []string `json:"tags"` // the first retentionTagsInReport
	Count      int      `json:"count"`
	FreedBytes int64    `json:"freed_bytes"`
	Error      string   `json:"error,omitempty"`
}

type RetentionWorkspaceReport struct {
	WorkspaceID uint `json:"workspace_id"`
	// Keep is the count this workspace ran with; 0 with Skipped means it opted out.
	Keep         int                   `json:"keep"`
	Skipped      bool                  `json:"skipped,omitempty"`
	Repositories []RetentionRepoReport `json:"repositories"`
	Count        int                   `json:"count"`
	FreedBytes   int64                 `json:"freed_bytes"`
}

// keepFor lets a workspace opt out or keep more, never fewer than the platform keeps.
func (p RetentionPolicy) keepFor(workspaceID uint) (int, bool) {
	o, ok := p.Overrides[workspaceID]
	if ok && o.Disabled {
		return 0, false
	}
	if ok && o.KeepImages > p.KeepImages {
		return o.KeepImages, true
	}
	return p.KeepImages, true
}

// Retention applies the policy to every workspace namespace; a dry run deletes nothing and reports what would go.
func (s *Service) Retention(ctx context.Context, p RetentionPolicy, dryRun bool, now time.Time) ([]RetentionWorkspaceReport, error) {
	if p.KeepImages < 1 {
		return nil, ErrBadRule
	}
	var keepDigests, keepRefs map[string]bool
	if s.catalog != nil {
		var err error
		if keepDigests, keepRefs, err = s.catalog.RecentReleaseRefs(p.ReleaseWindow); err != nil {
			// Without the rollback window a run could delete what a rollback needs.
			return nil, err
		}
	}
	ids, err := s.Namespaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RetentionWorkspaceReport, 0, len(ids))
	for _, wsID := range ids {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		keep, on := p.keepFor(wsID)
		rep := RetentionWorkspaceReport{WorkspaceID: wsID, Keep: keep, Skipped: !on, Repositories: []RetentionRepoReport{}}
		if on {
			opt := pickOptions{keepNewest: keep, olderThanDays: p.MinAgeDays, keepDigests: keepDigests, keepRefs: keepRefs, keepPatterns: p.ProtectedTags}
			s.retainWorkspace(ctx, wsID, opt, dryRun, now, &rep)
		}
		out = append(out, rep)
	}
	return out, nil
}

func (s *Service) retainWorkspace(ctx context.Context, wsID uint, opt pickOptions, dryRun bool, now time.Time, rep *RetentionWorkspaceReport) {
	repos, err := s.ListRepositories(ctx, wsID)
	if err != nil {
		rep.Repositories = append(rep.Repositories, RetentionRepoReport{Error: err.Error()})
		return
	}
	for _, r := range repos {
		if len(r.Tags) <= opt.keepNewest {
			continue
		}
		rr := RetentionRepoReport{Repository: r.Name, Tags: []string{}}
		for batch := 0; batch < retentionBatchesPerRepo; batch++ {
			sel, err := s.pick(ctx, wsID, r.Name, opt, now)
			if err != nil {
				rr.Error = err.Error()
				break
			}
			if len(sel.Tags) == 0 {
				break
			}
			res, err := s.DeleteTags(ctx, wsID, r.Name, sel.Tags, dryRun)
			if err != nil {
				rr.Error = err.Error()
				break
			}
			for _, t := range res.Removed {
				if len(rr.Tags) < retentionTagsInReport {
					rr.Tags = append(rr.Tags, t)
				}
			}
			rr.FreedBytes += res.FreedBytes
			if dryRun {
				// Nothing was deleted, so the next batch would pick the same tags; the count comes from the match.
				rr.Count = sel.Matched
				break
			}
			rr.Count += len(res.Removed)
			if len(res.Removed) == 0 || !sel.Truncated {
				break
			}
		}
		if rr.Count > 0 || rr.Error != "" {
			rep.Repositories = append(rep.Repositories, rr)
			rep.Count += rr.Count
			rep.FreedBytes += rr.FreedBytes
		}
	}
}

// RetentionStore persists the policy and the runs. Satisfied by *repositories.RegistryRetentionRepository.
type RetentionStore interface {
	Policy() (*models.RegistryRetention, error)
	Overrides() (map[uint]models.RegistryRetentionOverride, error)
	StartRun(run *models.RegistryRetentionRun, staleAfter time.Duration) (bool, error)
	FinishRun(run *models.RegistryRetentionRun) error
}

// RetentionRunner records each run; onWorkspace is told about every workspace a real run deleted from.
type RetentionRunner struct {
	svc         *Service
	store       RetentionStore
	onWorkspace func(rep RetentionWorkspaceReport)
	now         func() time.Time
}

func NewRetentionRunner(svc *Service, store RetentionStore, onWorkspace func(RetentionWorkspaceReport)) *RetentionRunner {
	return &RetentionRunner{svc: svc, store: store, onWorkspace: onWorkspace, now: time.Now}
}

// Scheduled is the nightly job; it does nothing while the policy is off.
func (r *RetentionRunner) Scheduled() error {
	p, err := r.store.Policy()
	if err != nil {
		return err
	}
	if !p.Enabled {
		return nil
	}
	run, policy, err := r.begin(false, "schedule")
	if errors.Is(err, ErrRetentionRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	r.execute(context.Background(), run, policy)
	return nil
}

// Start records the run and works in the background; a preview runs whether or not the policy is on.
func (r *RetentionRunner) Start(dryRun bool, trigger string) (*models.RegistryRetentionRun, error) {
	run, policy, err := r.begin(dryRun, trigger)
	if err != nil {
		return nil, err
	}
	go r.execute(context.Background(), run, policy)
	return run, nil
}

func (r *RetentionRunner) begin(dryRun bool, trigger string) (*models.RegistryRetentionRun, RetentionPolicy, error) {
	p, err := r.store.Policy()
	if err != nil {
		return nil, RetentionPolicy{}, err
	}
	overrides, err := r.store.Overrides()
	if err != nil {
		return nil, RetentionPolicy{}, err
	}
	run := &models.RegistryRetentionRun{DryRun: dryRun, Trigger: trigger, Status: models.RetentionRunRunning, StartedAt: r.now()}
	started, err := r.store.StartRun(run, retentionStaleRun)
	if err != nil {
		return nil, RetentionPolicy{}, err
	}
	if !started {
		return nil, RetentionPolicy{}, ErrRetentionRunning
	}
	return run, PolicyFrom(p, overrides), nil
}

func (r *RetentionRunner) execute(ctx context.Context, run *models.RegistryRetentionRun, p RetentionPolicy) {
	ctx, cancel := context.WithTimeout(ctx, retentionStaleRun)
	defer cancel()
	reports, err := r.svc.Retention(ctx, p, run.DryRun, r.now())
	for _, rep := range reports {
		run.Tags += rep.Count
		run.FreedBytes += rep.FreedBytes
		if !run.DryRun && rep.Count > 0 {
			metrics.RegistryRetentionDeleted(rep.Count)
			if r.onWorkspace != nil {
				r.onWorkspace(rep)
			}
		}
	}
	body, _ := json.Marshal(reports)
	run.Report = string(body)
	run.Status = models.RetentionRunSucceeded
	if err != nil {
		run.Status, run.Error = models.RetentionRunFailed, err.Error()
	}
	finished := r.now()
	run.FinishedAt = &finished
	if ferr := r.store.FinishRun(run); ferr != nil {
		logger.Warn("registry retention: recording the run failed", "run", run.ID, "error", ferr)
	}
	logger.Info("registry retention run finished", "run", run.ID, "dry_run", run.DryRun, "tags", run.Tags, "freed_bytes", run.FreedBytes, "error", run.Error)
}
