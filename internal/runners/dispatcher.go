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

package runners

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	imagesvc "github.com/miabi-io/miabi/internal/services/image"
	"github.com/miabi-io/miabi/internal/services/runner"
	"github.com/miabi-io/runner/proto"
)

// The run should be requeued.
var ErrRunnerOffline = errors.New("selected runner is offline")

// Must match worker.PipelineTopic; kept local to avoid an import cycle with the worker.
func runTopic(runID uint) string { return fmt.Sprintf("pipeline:%d", runID) }

type RunStore interface {
	UpdateRun(*models.PipelineRun) error
	UpdateStep(*models.PipelineStepRun) error
	SetRunImage(runID, imageID uint) error
}

type ImageRecorder interface {
	Record(in imagesvc.RecordInput) (*models.Image, error)
}

// Publisher streams a run's live logs/status. Satisfied by *eventbus.Bus.
type Publisher interface {
	Publish(topic string, e eventbus.Event)
}

// Nil-safe.
type VersionSettler interface {
	Settle(ctx context.Context, run *models.PipelineRun, digest, repository string, log func(string))
}

// The node pulls and runs the digest, never builds; nil means a build-only pipeline.
type Deployer interface {
	DeployByDigest(run *models.PipelineRun, appID uint, imageRef string) error
}

type Dispatcher struct {
	runners  *runner.Service
	sessions *Manager
	minter   *CredentialMinter
	runs     RunStore
	images   ImageRecorder
	bus      Publisher
	deployer Deployer
	versions VersionSettler
	secrets  SecretResolver
	logs     *logstore.Store // externalizes step logs on terminal (nil-safe)
	timeout  time.Duration   // per-job deadline (0 = none)
}

func (d *Dispatcher) SetDeployer(dep Deployer) { d.deployer = dep }

// SetVersions wires version settling, so a succeeded build is tagged with its version before it deploys.
func (d *Dispatcher) SetVersions(v VersionSettler) { d.versions = v }

// Without it, a pipeline that references a secret fails to dispatch.
func (d *Dispatcher) SetSecrets(r SecretResolver) { d.secrets = r }

// Without it only the bounded DB tail is kept; nil-safe.
func (d *Dispatcher) SetLogStore(s *logstore.Store) { d.logs = s }

// "" means a runner is actually available.
func (d *Dispatcher) RunnerWaitReason(workspaceID uint) string {
	return d.runners.AvailabilityReason(runner.Job{WorkspaceID: workspaceID})
}

// "" means a runner is actually available.
func (d *Dispatcher) WaitReason(in JobInputs) string {
	return d.runners.AvailabilityReason(runner.Job{
		WorkspaceID: in.Run.WorkspaceID, RequiredFeatures: requiredFeatures(in.Steps), MinVersion: requiredRunnerVersion(in.Steps),
	})
}

// A runner that died mid-job never releases its lease, which would otherwise starve its concurrency forever.
func (d *Dispatcher) SweepExpiredLeases(now time.Time) ([]models.RunnerLease, error) {
	return d.runners.SweepExpiredLeases(now)
}

func NewDispatcher(runners *runner.Service, sessions *Manager, minter *CredentialMinter, runs RunStore, images ImageRecorder, bus Publisher, timeout time.Duration) *Dispatcher {
	return &Dispatcher{runners: runners, sessions: sessions, minter: minter, runs: runs, images: images, bus: bus, timeout: timeout}
}

// dispatchMeta is the per-run context the frame loop needs beyond the run itself.
type dispatchMeta struct {
	runnerName  string   // provenance: which runner built the image
	repository  string   // fully-qualified image repo the runner pushed to
	appID       *uint    // owning application, for image provenance
	mask        []string // secret values to redact from live logs
	builtDigest string   // digest the build step pushed (set as frames arrive)
}

// ErrNoRunner and ErrRunnerOffline are returned unchanged so the caller can requeue.
func (d *Dispatcher) Dispatch(ctx context.Context, in JobInputs, requiredLabels []string, subjectUserID uint) error {
	run := in.Run
	rn, err := d.runners.SelectRunner(runner.Job{
		WorkspaceID: run.WorkspaceID, RequiredLabels: requiredLabels, RequiredFeatures: requiredFeatures(in.Steps),
		MinVersion: requiredRunnerVersion(in.Steps),
	})
	if err != nil {
		return err // ErrNoRunner → caller queues
	}

	in.Deadline = d.deadline()
	if d.minter != nil {
		creds, err := d.minter.Mint(subjectUserID, run.WorkspaceID, in.AppID, run.ID, in.Deadline)
		if err != nil {
			return fmt.Errorf("mint job credentials: %w", err)
		}
		defer d.minter.Revoke(creds) // dead the moment the run is terminal
		in.Creds = creds
	}
	spec, mask := BuildJobSpec(in)
	spec.Env = append(spec.Env, kv("MIABI_RUNNER_NAME", rn.Name))

	// A runner never touches the vault, and a missing secret must fail before a build burns its minutes.
	secretMask, err := resolveJobSecrets(d.secrets, run.WorkspaceID, &spec)
	if err != nil {
		return err
	}
	mask = append(mask, secretMask...)

	if _, err := d.runners.Lease(rn.ID, models.LeaseKindPipeline, run.ID, nil, in.Deadline); err != nil {
		return fmt.Errorf("lease runner %d: %w", rn.ID, err)
	}
	defer func() { _ = d.runners.ReleaseRun(models.LeaseKindPipeline, run.ID) }()

	run.RunnerID = &rn.ID
	_ = d.runs.UpdateRun(run)

	sess, ok := d.sessions.Session(rn.ID)
	if !ok {
		return ErrRunnerOffline
	}
	stream, err := sess.OpenStream()
	if err != nil {
		return fmt.Errorf("open runner stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	// Decode reads have no deadline, so trip the stream's read deadline on cancel or the job never times out.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.SetReadDeadline(time.Now())
		case <-stopWatch:
		}
	}()

	if err := proto.WriteJob(stream, spec); err != nil {
		return fmt.Errorf("send job: %w", err)
	}
	logger.Info("dispatched run to runner", "run", run.ID, "runner", rn.Name)

	meta := dispatchMeta{runnerName: rn.Name, repository: in.Repository, appID: in.AppID, mask: mask}
	_, err = d.processFrames(ctx, stream, run, in.Steps, meta)
	return err
}

func (d *Dispatcher) deadline() time.Time {
	if d.timeout <= 0 {
		return time.Time{}
	}
	return time.Now().Add(d.timeout)
}

// Returns an error if the stream ended before a terminal frame (a dead runner).
func (d *Dispatcher) processFrames(ctx context.Context, r io.Reader, run *models.PipelineRun, steps []models.PipelineStepRun, meta dispatchMeta) (models.PipelineRunStatus, error) {
	byOrdinal := make(map[int]*models.PipelineStepRun, len(steps))
	for i := range steps {
		byOrdinal[steps[i].Ordinal] = &steps[i]
	}
	// The live publish only feeds SSE, which is gone once the run is terminal, so logs are accumulated here.
	stepLogs := make(map[int]*strings.Builder)
	dec := json.NewDecoder(r)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var f proto.Frame
		if err := dec.Decode(&f); err != nil {
			// Persist logs first or the failed run has nothing to explain why it failed.
			d.persistStepLogs(run, byOrdinal, stepLogs)
			return "", fmt.Errorf("runner stream ended before completion: %w", err)
		}
		switch f.Type {
		case proto.FrameLog:
			line := redact(f.Line, meta.mask)
			buf(stepLogs, f.Step).WriteString(line + "\n")
			d.publish(run.ID, "log", line)
		case proto.FrameStep:
			if st := byOrdinal[f.Step]; st != nil {
				st.Status = mapStatus(f.Status)
				_ = d.runs.UpdateStep(st)
				d.publish(run.ID, "step", fmt.Sprintf("%d:%s", f.Step, st.Status))
			}
		case proto.FrameResult:
			meta.builtDigest = f.Digest // remember for the deploy-by-digest step
			d.recordImage(run, meta, f.Digest)
		case proto.FrameError:
			run.Status = models.PipelineRunFailed
			run.Error = f.Error
			d.finish(run)
			d.settleVersion(run, meta)
			d.persistStepLogs(run, byOrdinal, stepLogs)
			d.publish(run.ID, "status", string(models.PipelineRunFailed))
			return models.PipelineRunFailed, nil
		case proto.FrameDone:
			run.Status = mapStatus(f.Status)
			d.finish(run)
			// Before the deploy, so the version tag exists by the time the release records it.
			d.settleVersion(run, meta)
			d.persistStepLogs(run, byOrdinal, stepLogs)
			d.publish(run.ID, "status", string(run.Status))
			if run.Status == models.PipelineRunSucceeded {
				d.maybeDeploy(run, steps, meta)
			}
			return run.Status, nil
		}
	}
}

// settleTimeout bounds the registry calls that tag a published version.
const settleTimeout = 30 * time.Second

// Fresh context: the job's own may be past its deadline, and a successful build must still get its tag.
func (d *Dispatcher) settleVersion(run *models.PipelineRun, meta dispatchMeta) {
	if d.versions == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	d.versions.Settle(ctx, run, meta.builtDigest, meta.repository, func(line string) { d.publish(run.ID, "log", line) })
}

func buf(m map[int]*strings.Builder, ordinal int) *strings.Builder {
	b := m[ordinal]
	if b == nil {
		b = &strings.Builder{}
		m[ordinal] = b
	}
	return b
}

// Best-effort; Externalize is nil-safe so this works with the store disabled.
func (d *Dispatcher) persistStepLogs(run *models.PipelineRun, byOrdinal map[int]*models.PipelineStepRun, stepLogs map[int]*strings.Builder) {
	for ordinal, b := range stepLogs {
		st := byOrdinal[ordinal]
		if st == nil || b.Len() == 0 {
			continue
		}
		ref := logstore.PipelineStepRef(run.WorkspaceID, run.ID, ordinal)
		res, err := d.logs.Externalize(ref, b.String())
		if err != nil {
			logger.Warn("externalize step log failed", "run", run.ID, "step", ordinal, "error", err)
		}
		st.Logs = res.Tail
		st.LogRef = res.Ref
		st.LogBytes = res.Bytes
		st.LogLines = res.Lines
		st.LogTruncated = res.Truncated
		if err := d.runs.UpdateStep(st); err != nil {
			logger.Warn("persist step log failed", "run", run.ID, "step", ordinal, "error", err)
		}
	}
}

func (d *Dispatcher) recordImage(run *models.PipelineRun, meta dispatchMeta, digest string) {
	if digest == "" || d.images == nil {
		return
	}
	img, err := d.images.Record(imagesvc.RecordInput{
		WorkspaceID:   run.WorkspaceID,
		Repository:    meta.repository,
		Tag:           fmt.Sprintf("run-%d", run.Number),
		Digest:        digest,
		ApplicationID: meta.appID,
		PipelineRunID: &run.ID,
		Commit:        run.Commit,
		Runner:        meta.runnerName,
	})
	if err != nil {
		logger.Warn("record runner image failed", "run", run.ID, "error", err)
		return
	}
	run.ImageID = &img.ID
	_ = d.runs.SetRunImage(run.ID, img.ID)
}

func redact(line string, secrets []string) string {
	for _, s := range secrets {
		if s != "" {
			line = strings.ReplaceAll(line, s, "••••")
		}
	}
	return line
}

func (d *Dispatcher) maybeDeploy(run *models.PipelineRun, steps []models.PipelineStepRun, meta dispatchMeta) {
	if d.deployer == nil || meta.appID == nil || meta.builtDigest == "" || meta.repository == "" || !hasDeployStep(steps) {
		return
	}
	ref := meta.repository + "@" + meta.builtDigest
	if err := d.deployer.DeployByDigest(run, *meta.appID, ref); err != nil {
		logger.Warn("deploy-by-digest failed", "run", run.ID, "error", err)
		return
	}
	d.publish(run.ID, "log", "enqueued deploy-by-digest "+ref)
}

func hasDeployStep(steps []models.PipelineStepRun) bool {
	for i := range steps {
		if steps[i].Uses == "deploy" {
			return true
		}
	}
	return false
}

func (d *Dispatcher) publish(runID uint, kind, data string) {
	if d.bus == nil {
		return
	}
	d.bus.Publish(runTopic(runID), eventbus.Event{Type: kind, Data: data})
}

func (d *Dispatcher) publishRun(r *models.PipelineRun) {
	if d.bus == nil || r == nil {
		return
	}
	ev := map[string]any{"run_id": r.ID, "pipeline_id": r.PipelineID, "number": r.Number, "status": string(r.Status)}
	if r.StartedAt != nil {
		ev["started_at"] = r.StartedAt.UTC().Format(time.RFC3339)
	}
	if r.FinishedAt != nil {
		ev["finished_at"] = r.FinishedAt.UTC().Format(time.RFC3339)
	}
	d.bus.Publish(fmt.Sprintf("pipeline:ws:%d", r.WorkspaceID), eventbus.Event{Type: "run", Data: ev})
}

// Without an end timestamp every UI duration counts up from the start forever.
func (d *Dispatcher) finish(r *models.PipelineRun) {
	now := time.Now()
	r.FinishedAt = &now
	_ = d.runs.UpdateRun(r)
	d.publishRun(r)
}

// Unknown values map to failed (fail safe).
func mapStatus(s string) models.PipelineRunStatus {
	switch s {
	case "running":
		return models.PipelineRunRunning
	case "succeeded":
		return models.PipelineRunSucceeded
	case "canceled":
		return models.PipelineRunCanceled
	default:
		return models.PipelineRunFailed
	}
}
