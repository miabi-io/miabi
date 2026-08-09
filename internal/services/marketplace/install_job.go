// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Install phase keys. Stable identifiers the install-progress UI maps to a
// stepper; only the phases relevant to a template are present in a job.
const (
	PhaseVolumes   = "volumes"
	PhaseDatabases = "databases"
	PhaseApps      = "apps"
	PhaseConfigs   = "configs"
	PhaseConfig    = "config"
	PhaseDeploy    = "deploy"
)

// PhaseStatus is the state of a single install phase.
type PhaseStatus string

const (
	PhasePending PhaseStatus = "pending"
	PhaseActive  PhaseStatus = "active"
	PhaseDone    PhaseStatus = "done"
	PhaseError   PhaseStatus = "error"
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

// InstallJob is the live snapshot streamed to the install-progress UI: the
// ordered phases, a transient status line, and — once terminal — the install
// result (so the UI can navigate to the created app/stack) or an error.
type InstallJob struct {
	ID      string         `json:"id"`
	Status  JobStatus      `json:"status"`
	Phases  []InstallPhase `json:"phases"`
	Message string         `json:"message,omitempty"`
	Result  *InstallResult `json:"result,omitempty"`
	Error   string         `json:"error,omitempty"`

	workspaceID uint
	doneAt      time.Time
}

// snapshot returns a copy safe to publish/return outside the jobs lock (the
// Phases slice is cloned; Result is immutable once set).
func (j *InstallJob) snapshot() InstallJob {
	cp := *j
	cp.Phases = append([]InstallPhase(nil), j.Phases...)
	return cp
}

func installTopic(jobID string) string { return "install-job:" + jobID }

// jobRetention bounds how long a finished job (and its result) is kept for late
// subscribers / reconnects before being pruned.
const jobRetention = 15 * time.Minute

// installDeployTimeout caps how long a job waits for its apps to finish
// deploying before completing anyway (the UI still lands on the live app).
const installDeployTimeout = 15 * time.Minute

func newJobID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// buildPhases derives the stepper from the manifest, listing only the phases an
// install will actually perform.
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
		add(PhaseDeploy, "Deploying applications")
	}
	return p
}

// reporter records install progress onto a job and fans the snapshot out over
// the event bus. A nil *reporter is a no-op, so the synchronous Install path can
// share the same install() implementation without a job.
type reporter struct {
	s   *Service
	job *InstallJob
}

// phase transitions a named phase and publishes the new snapshot. Unknown keys
// (a phase the template skips) are ignored.
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

// StartInstall validates the request synchronously so bad input fails fast, then runs the install
// in the background and returns the initial job snapshot. Progress is streamed from StreamInstall;
// the UI completes when the job reports success or failure.
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
	// Hold the job open until the created apps come online, so the UI lands on a
	// live application instead of an empty "deploying" shell.
	if len(result.Apps) > 0 {
		s.waitForDeploy(ctx, workspaceID, result.Apps, rep)
	}
	rep.phase(PhaseDeploy, PhaseDone)
	s.finishJob(job, JobSucceeded, result, "")
}

// waitForDatabases polls newly provisioned instances until each settles, keeping the provisioning
// phase active with a live status line. Instances come up asynchronously, so without this apps would
// deploy against a database that isn't ready. Failed instances still resolve, so the install proceeds.
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

// waitForDeploy polls the created apps until each leaves the deploying state, surfacing a live status
// line as it goes. It never fails the install: a failed deploy still resolves the job so the UI
// navigates to the app, where the failure is visible.
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
			job.Phases[i].Status = PhaseDone
		}
	}
	job.doneAt = time.Now()
	snap := job.snapshot()
	s.jobsMu.Unlock()
	s.publishJob(snap)
}

// pruneLocked drops finished jobs older than the retention window. Caller holds
// jobsMu.
func (s *Service) pruneLocked() {
	cutoff := time.Now().Add(-jobRetention)
	for id, j := range s.jobs {
		if j.Status != JobRunning && !j.doneAt.IsZero() && j.doneAt.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
}

// InstallJobSnapshot returns a one-shot snapshot of a job (REST fallback for the
// SSE stream / reconnects).
func (s *Service) InstallJobSnapshot(jobID string) (InstallJob, bool) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job := s.jobs[jobID]
	if job == nil {
		return InstallJob{}, false
	}
	return job.snapshot(), true
}

// StreamInstall sends the job's current snapshot, then live updates, until the job is terminal or the
// client disconnects. The bool reports whether the job exists. Every event carries a full snapshot, so
// a missed update or a reconnect self-heals.
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
