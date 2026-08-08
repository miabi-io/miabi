// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// ErrRunnerOffline is returned when the scheduler picked a runner whose tunnel
// dropped between selection and dispatch (the run should be requeued).
var ErrRunnerOffline = errors.New("selected runner is offline")

// runTopic is the eventbus topic a pipeline run's live logs/status stream on.
// It MUST match worker.PipelineTopic (the SSE endpoint subscribes to it); kept
// local so this package need not import (and cycle with) the worker.
func runTopic(runID uint) string { return fmt.Sprintf("pipeline:%d", runID) }

// RunStore persists a run and its steps as report frames arrive. Satisfied by
// *repositories.PipelineRepository.
type RunStore interface {
	UpdateRun(*models.PipelineRun) error
	UpdateStep(*models.PipelineStepRun) error
	SetRunImage(runID, imageID uint) error
}

// ImageRecorder stamps provenance for an image a runner built and pushed by
// digest. Satisfied by *image.Service.
type ImageRecorder interface {
	Record(in imagesvc.RecordInput) (*models.Image, error)
}

// Publisher streams a run's live logs/status. Satisfied by *eventbus.Bus.
type Publisher interface {
	Publish(topic string, e eventbus.Event)
}

// Deployer performs a deploy-by-digest: it creates a deployment of the image a runner built and
// enqueues it to the target node, which pulls and runs it — the node never builds. Implemented by
// the worker's pipeline deployer; nil-safe (unset = a build-only pipeline that never deploys).
type Deployer interface {
	DeployByDigest(run *models.PipelineRun, appID uint, imageRef string) error
}

// Dispatcher runs a pipeline run on a runner: it selects and leases one, opens a stream over its
// tunnel, sends the JobSpec, and applies the report frames back onto the run. This is the
// control-plane counterpart to the runner's job loop, over the shared runner/proto contract.
type Dispatcher struct {
	runners  *runner.Service
	sessions *Manager
	minter   *CredentialMinter
	runs     RunStore
	images   ImageRecorder
	bus      Publisher
	deployer Deployer
	logs     *logstore.Store // externalizes step logs on terminal (nil-safe)
	timeout  time.Duration   // per-job deadline (0 = none)
}

// SetDeployer wires the deploy-by-digest hook, so a pipeline's terminal deploy
// step rolls the runner-built image out to the target node (nil-safe).
func (d *Dispatcher) SetDeployer(dep Deployer) { d.deployer = dep }

// SetLogStore wires the shared log store so a pipeline step's full log is
// externalized (with a bounded DB tail) when the run reaches a terminal state.
// Without it, only the bounded tail is kept in the step's DB row. nil-safe.
func (d *Dispatcher) SetLogStore(s *logstore.Store) { d.logs = s }

// RunnerWaitReason explains why no runner is currently available for a build in
// workspaceID, for the deploy's "waiting for a runner" message. "" means one is
// actually available.
func (d *Dispatcher) RunnerWaitReason(workspaceID uint) string {
	return d.runners.AvailabilityReason(runner.Job{WorkspaceID: workspaceID})
}

// SweepExpiredLeases releases every active lease whose deadline has passed and returns them. A
// runner that died mid-job never runs the lease's release defer, so without this the lease counts
// against its concurrency forever and starves it. The affected runs re-attempt on their own.
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

// Dispatch selects and leases a runner for the run, mints its per-job credentials, sends the job,
// and processes the report stream to completion, revoking credentials and releasing the lease on
// the way out. ErrNoRunner and ErrRunnerOffline are returned unchanged so the caller can requeue.
func (d *Dispatcher) Dispatch(ctx context.Context, in JobInputs, requiredLabels []string, subjectUserID uint) error {
	run := in.Run
	rn, err := d.runners.SelectRunner(runner.Job{WorkspaceID: run.WorkspaceID, RequiredLabels: requiredLabels})
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

	// processFrames blocks on stream reads (json.Decode); a read has no deadline of its own, so a cancelled
	// ctx (per-job timeout) wouldn't take effect until the runner sent bytes. Trip the stream's read deadline
	// on cancellation to unblock the pending read so the job actually times out.
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

// deadline returns the absolute per-job deadline, or the zero time when no
// timeout is configured.
func (d *Dispatcher) deadline() time.Time {
	if d.timeout <= 0 {
		return time.Time{}
	}
	return time.Now().Add(d.timeout)
}

// processFrames applies the runner's report stream onto the run: live logs and status go to the
// eventbus, step transitions and the built digest are persisted, and the terminal frame sets the
// final status. Returns an error if the stream ended before a terminal frame (a dead runner).
func (d *Dispatcher) processFrames(ctx context.Context, r io.Reader, run *models.PipelineRun, steps []models.PipelineStepRun, meta dispatchMeta) (models.PipelineRunStatus, error) {
	byOrdinal := make(map[int]*models.PipelineStepRun, len(steps))
	for i := range steps {
		byOrdinal[steps[i].Ordinal] = &steps[i]
	}
	// Accumulate each step's log so it can be persisted (externalized + a DB tail)
	// when the run finishes — the live publish below only feeds the SSE stream,
	// which is gone once the run is terminal. Keyed by the frame's step ordinal.
	stepLogs := make(map[int]*strings.Builder)
	dec := json.NewDecoder(r)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var f proto.Frame
		if err := dec.Decode(&f); err != nil {
			// Stream ended without a terminal frame (dead/disconnected runner). The
			// caller fails the run, so persist whatever the steps logged first —
			// otherwise the failed run has no logs to explain why it failed.
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
			d.persistStepLogs(run, byOrdinal, stepLogs)
			d.publish(run.ID, "status", string(models.PipelineRunFailed))
			return models.PipelineRunFailed, nil
		case proto.FrameDone:
			run.Status = mapStatus(f.Status)
			d.finish(run)
			d.persistStepLogs(run, byOrdinal, stepLogs)
			d.publish(run.ID, "status", string(run.Status))
			if run.Status == models.PipelineRunSucceeded {
				d.maybeDeploy(run, steps, meta)
			}
			return run.Status, nil
		}
	}
}

func buf(m map[int]*strings.Builder, ordinal int) *strings.Builder {
	b := m[ordinal]
	if b == nil {
		b = &strings.Builder{}
		m[ordinal] = b
	}
	return b
}

// persistStepLogs writes each step's accumulated log to its DB row so it survives the run: the
// full log to the log store with a bounded tail in the Logs column, or just the tail when the
// store is disabled. Externalize is nil-safe, so this works either way. Best-effort.
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

// recordImage stamps provenance for a digest the runner pushed, linking it to the
// run and marking which runner built it, then points the run at it.
func (d *Dispatcher) recordImage(run *models.PipelineRun, meta dispatchMeta, digest string) {
	if digest == "" || d.images == nil {
		return
	}
	img, err := d.images.Record(imagesvc.RecordInput{
		WorkspaceID:   run.WorkspaceID,
		Repository:    meta.repository,
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

// redact replaces every secret value in a log line with a fixed mask, so a step
// that echoes an injected credential prints ••••.
func redact(line string, secrets []string) string {
	for _, s := range secrets {
		if s != "" {
			line = strings.ReplaceAll(line, s, "••••")
		}
	}
	return line
}

// maybeDeploy enqueues a deploy-by-digest for a succeeded run that built an image and has a deploy
// step, so the target node pulls and runs the exact digest the runner produced. A build-only
// pipeline is a no-op.
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

// finish stamps the terminal timestamp before saving. Without it the run has no
// end, and every duration in the UI counts up from its start forever.
func (d *Dispatcher) finish(r *models.PipelineRun) {
	now := time.Now()
	r.FinishedAt = &now
	_ = d.runs.UpdateRun(r)
	d.publishRun(r)
}

// mapStatus maps a runner's status string onto the control plane's run status,
// defaulting an unknown value to failed (fail safe).
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
