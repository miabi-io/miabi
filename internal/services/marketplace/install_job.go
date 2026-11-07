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

package marketplace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

// Stable identifiers the progress UI maps to a stepper; only phases a template uses are present.
const (
	PhaseVolumes   = "volumes"
	PhaseDatabases = "databases"
	PhaseApps      = "apps"
	PhaseConfigs   = "configs"
	PhaseConfig    = "config"
	PhaseDeploy    = "deploy"
	PhaseRoutes    = "routes"
)

// PhaseStatus is the state of a single install phase.
type PhaseStatus string

const (
	PhasePending PhaseStatus = "pending"
	PhaseActive  PhaseStatus = "active"
	PhaseDone    PhaseStatus = "done"
	PhaseError   PhaseStatus = "error"
	// PhaseWarning is a finished phase that skipped part of its work, e.g. a route with no domain.
	PhaseWarning PhaseStatus = "warning"
)

// JobStatus is the overall state of an install job.
type JobStatus string

const (
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

// InstallPhase is one step of an install, rendered as a stepper row.
type InstallPhase struct {
	Key    string      `json:"key"`
	Label  string      `json:"label"`
	Status PhaseStatus `json:"status"`
}

type InstallJob struct {
	ID      string         `json:"id"`
	Status  JobStatus      `json:"status"`
	Phases  []InstallPhase `json:"phases"`
	Message string         `json:"message,omitempty"`
	// Warnings are non-fatal problems the install worked around, shown after it completes.
	Warnings []string       `json:"warnings,omitempty"`
	Result   *InstallResult `json:"result,omitempty"`
	Error    string         `json:"error,omitempty"`

	workspaceID uint
	doneAt      time.Time
}

// Phases is cloned and Result is immutable once set, so the copy is safe outside the lock.
func (j *InstallJob) snapshot() InstallJob {
	cp := *j
	cp.Phases = append([]InstallPhase(nil), j.Phases...)
	cp.Warnings = append([]string(nil), j.Warnings...)
	return cp
}

func installTopic(jobID string) string { return "install-job:" + jobID }

// Kept for late subscribers and reconnects before pruning.
const jobRetention = 15 * time.Minute

// After the timeout the job completes anyway; the UI still lands on the live app.
const installDeployTimeout = 15 * time.Minute

func newJobID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func buildPhases(m *manifest.Manifest) []InstallPhase {
	var p []InstallPhase
	add := func(key, label string) { p = append(p, InstallPhase{Key: key, Label: label, Status: PhasePending}) }
	if len(m.Volumes) > 0 {
		add(PhaseVolumes, "Creating volumes")
	}
	if len(m.Databases) > 0 {
		add(PhaseDatabases, "Provisioning database")
	}
	if len(m.Applications) > 0 {
		add(PhaseApps, "Creating applications")
		if len(m.Configs) > 0 {
			add(PhaseConfigs, "Creating configuration files")
		}
		add(PhaseConfig, "Configuring environment & secrets")
		if len(m.Routes) > 0 {
			add(PhaseRoutes, "Creating routes")
		}
		add(PhaseDeploy, "Deploying applications")
	}
	return p
}

// A nil reporter is a no-op so the synchronous Install path shares install() without a job.
type reporter struct {
	s   *Service
	job *InstallJob
}

// Unknown keys (phases the template skips) are ignored.
func (r *reporter) phase(key string, st PhaseStatus) {
	if r == nil {
		return
	}
	r.s.jobsMu.Lock()
	for i := range r.job.Phases {
		if r.job.Phases[i].Key == key {
			r.job.Phases[i].Status = st
		}
	}
	snap := r.job.snapshot()
	r.s.jobsMu.Unlock()
	r.s.publishJob(snap)
}

func (r *reporter) warn(msg string) {
	if r == nil {
		return
	}
	r.s.jobsMu.Lock()
	r.job.Warnings = append(r.job.Warnings, msg)
	snap := r.job.snapshot()
	r.s.jobsMu.Unlock()
	r.s.publishJob(snap)
}

func (r *reporter) note(msg string) {
	if r == nil {
		return
	}
	r.s.jobsMu.Lock()
	r.job.Message = msg
	snap := r.job.snapshot()
	r.s.jobsMu.Unlock()
	r.s.publishJob(snap)
}

func (s *Service) publishJob(snap InstallJob) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(installTopic(snap.ID), eventbus.Event{Type: "job", Data: snap})
}

// StartInstall validates synchronously so bad input fails fast, then installs in the background.
func (s *Service) StartInstall(workspaceID uint, in InstallInput) (InstallJob, error) {
	m, _, ok := s.resolveManifest(workspaceID, in.Name, in.Version)
	if !ok {
		return InstallJob{}, ErrTemplateNotFound
	}
	if _, _, err := resolveInputs(m, in.Inputs); err != nil {
		return InstallJob{}, err
	}

	job := &InstallJob{
		ID:          newJobID(),
		Status:      JobRunning,
		Phases:      buildPhases(m),
		workspaceID: workspaceID,
	}

	s.jobsMu.Lock()
	if s.jobs == nil {
		s.jobs = map[string]*InstallJob{}
	}
	s.pruneLocked()
	s.jobs[job.ID] = job
	snap := job.snapshot()
	s.jobsMu.Unlock()

	go s.runInstall(job, workspaceID, in)
	return snap, nil
}

func (s *Service) runInstall(job *InstallJob, workspaceID uint, in InstallInput) {
	ctx, cancel := context.WithTimeout(context.Background(), installDeployTimeout)
	defer cancel()

	rep := &reporter{s: s, job: job}
	result, err := s.install(ctx, workspaceID, in, rep)
	if err != nil {
		s.finishJob(job, JobFailed, nil, err.Error())
		return
	}
	// So the UI lands on a live application instead of an empty "deploying" shell.
	if len(result.Apps) > 0 {
		s.waitForDeploy(ctx, workspaceID, result.Apps, rep)
	}
	rep.phase(PhaseDeploy, PhaseDone)
	s.finishJob(job, JobSucceeded, result, "")
}

// Instances come up async, so without this apps would deploy against an unready database; failures still resolve.
func (s *Service) waitForDatabases(ctx context.Context, workspaceID uint, insts []*models.DatabaseInstance, rep *reporter) {
	names := make(map[uint]string, len(insts))
	ids := make([]uint, 0, len(insts))
	for _, in := range insts {
		ids = append(ids, in.ID)
		names[in.ID] = in.Name
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		pending, pendingName := 0, ""
		for _, id := range ids {
			inst, err := s.dbs.Get(workspaceID, id)
			if err != nil {
				continue
			}
			switch inst.Status {
			case models.DBStatusRunning, models.DBStatusFailed, models.DBStatusStopped:
			default:
				pending++
				pendingName = names[id]
			}
		}
		if pending == 0 {
			return
		}
		if pending == 1 {
			rep.note("Provisioning " + pendingName + "…")
		} else {
			rep.note(fmt.Sprintf("Provisioning %d databases…", pending))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// waitForDeploy never fails the install: a failed deploy still resolves the job so the UI opens the app.
func (s *Service) waitForDeploy(ctx context.Context, workspaceID uint, apps []*models.Application, rep *reporter) {
	names := make(map[uint]string, len(apps))
	ids := make([]uint, 0, len(apps))
	for _, a := range apps {
		ids = append(ids, a.ID)
		names[a.ID] = a.Name
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		pending, pendingName := 0, ""
		for _, id := range ids {
			app, err := s.apps.Get(workspaceID, id)
			if err != nil {
				continue
			}
			switch app.Status {
			case models.AppStatusRunning, models.AppStatusFailed, models.AppStatusStopped:
			default:
				pending++
				pendingName = names[id]
			}
		}
		if pending == 0 {
			return
		}
		if pending == 1 {
			rep.note("Deploying " + pendingName + "…")
		} else {
			rep.note(fmt.Sprintf("Deploying %d applications…", pending))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) finishJob(job *InstallJob, st JobStatus, result *InstallResult, errMsg string) {
	s.jobsMu.Lock()
	job.Status = st
	job.Result = result
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
			if job.Phases[i].Status != PhaseWarning {
				job.Phases[i].Status = PhaseDone
			}
		}
	}
	job.doneAt = time.Now()
	snap := job.snapshot()
	s.jobsMu.Unlock()
	s.publishJob(snap)
}

// Caller holds jobsMu.
func (s *Service) pruneLocked() {
	cutoff := time.Now().Add(-jobRetention)
	for id, j := range s.jobs {
		if j.Status != JobRunning && !j.doneAt.IsZero() && j.doneAt.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
}

func (s *Service) InstallJobSnapshot(jobID string) (InstallJob, bool) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job := s.jobs[jobID]
	if job == nil {
		return InstallJob{}, false
	}
	return job.snapshot(), true
}

// Every event carries a full snapshot, so a missed update or reconnect self-heals.
func (s *Service) StreamInstall(ctx context.Context, jobID string, send func(eventbus.Event) error) (bool, error) {
	s.jobsMu.Lock()
	job := s.jobs[jobID]
	var snap InstallJob
	if job != nil {
		snap = job.snapshot()
	}
	s.jobsMu.Unlock()
	if job == nil {
		return false, nil
	}

	if s.bus == nil {
		return true, send(eventbus.Event{Type: "job", Data: snap})
	}

	// Subscribe before sending the snapshot so no update slips through the gap.
	ch, unsubscribe := s.bus.Subscribe(installTopic(jobID))
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
			if je, ok := e.Data.(InstallJob); ok && je.Status != JobRunning {
				return true, nil
			}
		}
	}
}
