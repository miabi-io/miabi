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

package account

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/miabi-io/miabi/internal/services/eventbus"
)

// Stable keys the UI maps to a stepper; only phases the workspace needs are present.
const (
	PhaseApps      = "apps"
	PhaseDatabases = "databases"
	PhaseStacks    = "stacks"
	PhaseVolumes   = "volumes"
	PhaseFinalize  = "finalize"
)

// PhaseStatus is the state of a single deletion phase.
type PhaseStatus string

const (
	PhasePending PhaseStatus = "pending"
	PhaseActive  PhaseStatus = "active"
	PhaseDone    PhaseStatus = "done"
	PhaseError   PhaseStatus = "error"
)

// JobStatus is the overall state of a deletion job.
type JobStatus string

const (
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

// DeletionPhase is one step of a workspace teardown, rendered as a stepper row.
type DeletionPhase struct {
	Key    string      `json:"key"`
	Label  string      `json:"label"`
	Status PhaseStatus `json:"status"`
}

type DeletionJob struct {
	ID      string          `json:"id"`
	Status  JobStatus       `json:"status"`
	Phases  []DeletionPhase `json:"phases"`
	Message string          `json:"message,omitempty"`
	Error   string          `json:"error,omitempty"`

	workspaceID uint
	doneAt      time.Time
}

func (j *DeletionJob) snapshot() DeletionJob {
	cp := *j
	cp.Phases = append([]DeletionPhase(nil), j.Phases...)
	return cp
}

func deletionTopic(jobID string) string { return "workspace-deletion:" + jobID }

// Kept for late subscribers and reconnects before pruning.
const delJobRetention = 15 * time.Minute

// Best-effort deletes can hang on an unresponsive Docker daemon.
const workspaceDeletionTimeout = 15 * time.Minute

func newDelJobID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) buildDeletionPhases(wsID uint) []DeletionPhase {
	var p []DeletionPhase
	add := func(key, label string) { p = append(p, DeletionPhase{Key: key, Label: label, Status: PhasePending}) }
	if apps, _ := s.apps.ListByWorkspace(wsID); len(apps) > 0 {
		add(PhaseApps, "Stopping and removing applications")
	}
	if insts, _ := s.dbs.ListByWorkspace(wsID); len(insts) > 0 {
		add(PhaseDatabases, "Removing databases")
	}
	if stacks, _ := s.stacks.ListByWorkspace(wsID); len(stacks) > 0 {
		add(PhaseStacks, "Removing stacks")
	}
	if vols, _ := s.storageOps.List(wsID); len(vols) > 0 {
		add(PhaseVolumes, "Deleting volumes")
	}
	add(PhaseFinalize, "Finalizing workspace removal")
	return p
}

// A nil reporter is a no-op so DeleteWorkspaceNow can share teardownResources without a job.
type delReporter struct {
	s   *Service
	job *DeletionJob
}

// Unknown keys (skipped phases) are ignored.
func (r *delReporter) phase(key string, st PhaseStatus) {
	if r == nil {
		return
	}
	r.s.delJobsMu.Lock()
	for i := range r.job.Phases {
		if r.job.Phases[i].Key == key {
			r.job.Phases[i].Status = st
		}
	}
	snap := r.job.snapshot()
	r.s.delJobsMu.Unlock()
	r.s.publishDelJob(snap)
}

func (r *delReporter) note(msg string) {
	if r == nil {
		return
	}
	r.s.delJobsMu.Lock()
	r.job.Message = msg
	snap := r.job.snapshot()
	r.s.delJobsMu.Unlock()
	r.s.publishDelJob(snap)
}

func (s *Service) publishDelJob(snap DeletionJob) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(deletionTopic(snap.ID), eventbus.Event{Type: "job", Data: snap})
}

// Validates synchronously so a system workspace or missing id fails fast, then tears down in the background.
func (s *Service) StartWorkspaceDeletion(wsID uint) (DeletionJob, error) {
	ws, err := s.workspaces.FindByID(wsID)
	if err != nil {
		return DeletionJob{}, err
	}
	if ws.System {
		return DeletionJob{}, ErrSystemProtected
	}

	job := &DeletionJob{
		ID:          newDelJobID(),
		Status:      JobRunning,
		Phases:      s.buildDeletionPhases(wsID),
		workspaceID: wsID,
	}

	s.delJobsMu.Lock()
	if s.delJobs == nil {
		s.delJobs = map[string]*DeletionJob{}
	}
	s.pruneDelLocked()
	s.delJobs[job.ID] = job
	snap := job.snapshot()
	s.delJobsMu.Unlock()

	go s.runWorkspaceDeletion(job)
	return snap, nil
}

func (s *Service) runWorkspaceDeletion(job *DeletionJob) {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceDeletionTimeout)
	defer cancel()

	rep := &delReporter{s: s, job: job}
	s.teardownResources(ctx, job.workspaceID, rep)

	rep.phase(PhaseFinalize, PhaseActive)
	rep.note("Removing workspace…")
	if err := s.finalizeWorkspace(job.workspaceID); err != nil {
		s.finishDelJob(job, JobFailed, err.Error())
		return
	}
	rep.phase(PhaseFinalize, PhaseDone)
	s.finishDelJob(job, JobSucceeded, "")
}

func (s *Service) finishDelJob(job *DeletionJob, st JobStatus, errMsg string) {
	s.delJobsMu.Lock()
	job.Status = st
	job.Error = errMsg
	job.Message = ""
	if st == JobFailed {
		for i := range job.Phases {
			if job.Phases[i].Status == PhaseActive {
				job.Phases[i].Status = PhaseError
			}
		}
	} else {
		for i := range job.Phases {
			job.Phases[i].Status = PhaseDone
		}
	}
	job.doneAt = time.Now()
	snap := job.snapshot()
	s.delJobsMu.Unlock()
	s.publishDelJob(snap)
}

// Caller holds delJobsMu.
func (s *Service) pruneDelLocked() {
	cutoff := time.Now().Add(-delJobRetention)
	for id, j := range s.delJobs {
		if j.Status != JobRunning && !j.doneAt.IsZero() && j.doneAt.Before(cutoff) {
			delete(s.delJobs, id)
		}
	}
}

// REST fallback for the SSE stream and reconnects.
func (s *Service) DeletionJobSnapshot(jobID string) (DeletionJob, bool) {
	s.delJobsMu.Lock()
	defer s.delJobsMu.Unlock()
	job := s.delJobs[jobID]
	if job == nil {
		return DeletionJob{}, false
	}
	return job.snapshot(), true
}

// Every event carries a full snapshot, so a missed update or reconnect self-heals.
func (s *Service) StreamWorkspaceDeletion(ctx context.Context, jobID string, send func(eventbus.Event) error) (bool, error) {
	s.delJobsMu.Lock()
	job := s.delJobs[jobID]
	var snap DeletionJob
	if job != nil {
		snap = job.snapshot()
	}
	s.delJobsMu.Unlock()
	if job == nil {
		return false, nil
	}

	if s.bus == nil {
		return true, send(eventbus.Event{Type: "job", Data: snap})
	}

	// Subscribe before sending the snapshot so no update slips through the gap.
	ch, unsubscribe := s.bus.Subscribe(deletionTopic(jobID))
	defer unsubscribe()
	if err := send(eventbus.Event{Type: "job", Data: snap}); err != nil {
		return true, err
	}
	if snap.Status != JobRunning {
		return true, nil
	}
	for {
		select {
		case <-ctx.Done():
			return true, nil
		case e, ok := <-ch:
			if !ok {
				return true, nil
			}
			if err := send(e); err != nil {
				return true, err
			}
			if je, ok := e.Data.(DeletionJob); ok && je.Status != JobRunning {
				return true, nil
			}
		}
	}
}
