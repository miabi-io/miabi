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

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	imagesvc "github.com/miabi-io/miabi/internal/services/image"
	"github.com/miabi-io/miabi/internal/services/pipeline"
	"github.com/miabi-io/miabi/internal/storage/repositories"

	"errors"

	"github.com/miabi-io/miabi/internal/runners"
	"github.com/miabi-io/miabi/internal/services/registryserver"
	runnersvc "github.com/miabi-io/miabi/internal/services/runner"
)

// PipelineTopic carries a pipeline run's live step logs and status.
func PipelineTopic(runID uint) string { return fmt.Sprintf("pipeline:%d", runID) }

// PipelineWorkspaceTopic carries every run transition in a workspace.
func PipelineWorkspaceTopic(workspaceID uint) string {
	return fmt.Sprintf("pipeline:ws:%d", workspaceID)
}

// RunEvent lets a list patch the row in place without refetching.
type RunEvent struct {
	RunID      uint   `json:"run_id"`
	PipelineID uint   `json:"pipeline_id"`
	Number     int    `json:"number"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type PipelineHandler struct {
	pipelines   *repositories.PipelineRepository
	apps        *repositories.ApplicationRepository
	deployments *repositories.DeploymentRepository
	gitRepos    *repositories.GitRepoRepository
	images      *imagesvc.Service
	clients     NodeDocker
	bus         *eventbus.Bus
	producer    *Producer
	logs        *logstore.Store
	secrets     SecretResolver

	// Every pipeline build runs on a registered runner; there is no on-node fallback.
	dispatcher        RunnerDispatcher
	workspaces        *repositories.WorkspaceRepository
	registry          string               // fallback registry host (raw MIABI_REGISTRY_HOST)
	registryHosts     RegistryHostResolver // resolves the live host (UI-/domain-derived)
	runnerWaitTimeout time.Duration        // how long a run waits for a runner before failing
	versions          VersionKeeper
}

// The version tag is checked free before dispatch and given back if the run fails before a runner reports.
func (h *PipelineHandler) SetVersions(v VersionKeeper) { h.versions = v }

// VersionKeeper checks and settles a run's reserved version. Satisfied by *versioning.Service.
type VersionKeeper interface {
	VerifyTagFree(ctx context.Context, run *models.PipelineRun, repository string, log func(string)) error
	Settle(ctx context.Context, run *models.PipelineRun, digest, repository string, log func(string))
}

// Tracks the live host so the login and push hosts stay identical; a mismatch yields "denied".
type RegistryHostResolver interface{ RegistryHost() string }

const builderHandoffTimeout = 10 * time.Minute

func (h *PipelineHandler) registryHost() string {
	if h.registryHosts != nil {
		if host := h.registryHosts.RegistryHost(); host != "" {
			return host
		}
	}
	return h.registry
}

type RunnerDispatcher interface {
	Dispatch(ctx context.Context, in runners.JobInputs, requiredLabels []string, subjectUserID uint) error
	// WaitReason explains why no runner can take the run now, or "".
	WaitReason(in runners.JobInputs) string
}

// A run with no available runner waits up to runnerWaitTimeout rather than building on a node.
func (h *PipelineHandler) SetRunnerDispatch(d RunnerDispatcher, workspaces *repositories.WorkspaceRepository, registry string, hosts RegistryHostResolver, runnerWaitTimeout time.Duration) {
	h.dispatcher = d
	h.workspaces = workspaces
	h.registry = registry
	h.registryHosts = hosts
	h.runnerWaitTimeout = runnerWaitTimeout
}

// When set, full step logs go to the store on terminal state and the DB row keeps a bounded tail.
func (h *PipelineHandler) SetLogStore(s *logstore.Store) { h.logs = s }

// Lets a Git credential that references a workspace Secret be resolved; nil = literal credentials only.
func (h *PipelineHandler) SetSecrets(s SecretResolver) { h.secrets = s }

// NewPipelineHandler builds the internal runner handler.
func NewPipelineHandler(
	pipelines *repositories.PipelineRepository,
	apps *repositories.ApplicationRepository,
	deployments *repositories.DeploymentRepository,
	gitRepos *repositories.GitRepoRepository,
	images *imagesvc.Service,
	clients NodeDocker,
	bus *eventbus.Bus,
	producer *Producer,
) *PipelineHandler {
	return &PipelineHandler{
		pipelines: pipelines, apps: apps, deployments: deployments,
		gitRepos: gitRepos, images: images, clients: clients, bus: bus, producer: producer,
	}
}

// ProcessTask runs a pipeline end to end.
func (h *PipelineHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p RunPipelinePayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad pipeline payload: %w", err)
	}
	run, err := h.pipelines.FindRunByID(p.PipelineRunID)
	if err != nil {
		return fmt.Errorf("pipeline run %d not found: %w", p.PipelineRunID, err)
	}
	if run.Status.IsTerminal() {
		return nil
	}
	def, err := h.pipelines.FindInWorkspace(run.WorkspaceID, run.PipelineID)
	if err != nil {
		return h.failRun(run, fmt.Errorf("pipeline %d not found: %w", run.PipelineID, err))
	}
	// Validate the spec before dispatching (a bad spec fails fast).
	if _, perr := pipeline.ParseSpec([]byte(def.Spec)); perr != nil {
		return h.failRun(run, fmt.Errorf("invalid pipeline spec: %w", perr))
	}

	// A standalone worker without runner tunnels hands the run to the control-plane worker; checked before marking running.
	if h.dispatcher == nil {
		return h.handOffToBuilder(run)
	}

	now := time.Now()
	run.Status = models.PipelineRunRunning
	run.StartedAt = &now
	_ = h.pipelines.UpdateRun(run)
	h.publishStatus(run.ID, models.PipelineRunRunning)

	return h.runOnRunner(ctx, run, def)
}

func (h *PipelineHandler) handOffToBuilder(run *models.PipelineRun) error {
	if time.Since(run.CreatedAt) > builderHandoffTimeout {
		return h.failRun(run, fmt.Errorf(
			"no runner-capable worker picked this run up within %s — check that the control-plane server is running",
			builderHandoffTimeout))
	}
	run.Status = models.PipelineRunPending
	run.StartedAt = nil
	_ = h.pipelines.UpdateRun(run)
	h.log(run.ID, "handing the run off to the runner-capable worker…")
	h.publishStatus(run.ID, models.PipelineRunPending)
	return h.producer.EnqueuePipelineRunToBuilder(run.ID, runnerWaitInterval)
}

func (h *PipelineHandler) runOnRunner(ctx context.Context, run *models.PipelineRun, def *models.PipelineDefinition) error {
	in, err := h.jobInputs(run, def)
	if err != nil {
		return h.failRun(run, err)
	}
	if h.versions != nil {
		if err := h.versions.VerifyTagFree(ctx, run, in.Repository, func(line string) { h.log(run.ID, line) }); err != nil {
			return h.failRun(run, err)
		}
	}
	err = h.dispatcher.Dispatch(ctx, in, nil, subjectUser(run))
	switch {
	case err == nil:
		h.markCacheBuilt(run, in)
		return nil // Dispatch drove the run to a terminal status
	case errors.Is(err, runnersvc.ErrNoRunner), errors.Is(err, runners.ErrRunnerOffline):
		return h.waitForRunner(run, h.dispatcher.WaitReason(in)) // none available right now — wait (bounded)
	default:
		return h.failRun(run, err)
	}
}

func (h *PipelineHandler) markCacheBuilt(run *models.PipelineRun, in runners.JobInputs) {
	if h.apps == nil || in.AppID == nil || run.Status != models.PipelineRunSucceeded || run.ImageID == nil {
		return
	}
	if err := h.apps.MarkCacheBuilt(*in.AppID, in.CacheGeneration); err != nil {
		logger.Warn("mark build cache generation failed", "app", *in.AppID, "error", err)
	}
}

// runnerWaitInterval is how long a run waits before re-checking for a free runner.
const runnerWaitInterval = 15 * time.Second

// Fails after runnerWaitTimeout from creation rather than waiting forever.
func (h *PipelineHandler) waitForRunner(run *models.PipelineRun, reason string) error {
	if h.runnerWaitTimeout > 0 && time.Since(run.CreatedAt) > h.runnerWaitTimeout {
		if reason == "" {
			reason = "register a runner (Settings → Runners)"
		}
		return h.failRun(run, fmt.Errorf("no runner became available within %s — %s", h.runnerWaitTimeout, reason))
	}
	run.Status = models.PipelineRunPending
	run.StartedAt = nil
	_ = h.pipelines.UpdateRun(run)
	if reason != "" {
		h.log(run.ID, "waiting for an available runner: "+reason)
	} else {
		h.log(run.ID, "waiting for an available runner…")
	}
	h.publishStatus(run.ID, models.PipelineRunPending)
	return h.producer.EnqueuePipelineRunIn(run.ID, runnerWaitInterval)
}

func (h *PipelineHandler) jobInputs(run *models.PipelineRun, def *models.PipelineDefinition) (runners.JobInputs, error) {
	// Login and push hosts must match, or the registry answers "denied".
	reg := h.registryHost()
	var in runners.JobInputs
	// Push under the immutable ws_<id> namespace: the recorded ref is re-pulled later and a rename would orphan it.
	ns := registryserver.Namespace(run.WorkspaceID)
	if h.workspaces != nil {
		if ws, err := h.workspaces.FindByID(run.WorkspaceID); err == nil && ws.Name != "" {
			in.WorkspaceName = ws.Name
		}
	}
	in.Run = run
	in.Pipeline = def.Name
	in.Steps = run.Steps
	in.Env = run.Env
	in.Registry = reg
	in.Ref = run.Commit
	in.Branch = run.Branch
	switch def.Checkout() {
	case models.CheckoutApplication:
		in.AppID = def.ApplicationID
		app, err := h.apps.FindInWorkspace(run.WorkspaceID, *def.ApplicationID)
		if err != nil {
			return runners.JobInputs{}, fmt.Errorf("pipeline application %d: %w", *def.ApplicationID, err)
		}
		in.AppName = app.Name
		su, err := h.sourceURL(app)
		if err != nil {
			return runners.JobInputs{}, err
		}
		in.SourceURL = su
		// The cache is the app's, so a pipeline run and a direct deploy of the same app share it.
		in.CacheGeneration = app.CacheGeneration
		in.CacheTrunk = app.GitRef
		in.CacheCold = app.CacheBuiltGeneration != app.CacheGeneration
		if reg != "" {
			// The deploy path recognizes <host>/ws_<id>/<app> as a build ref and resolves ownership from it.
			in.Repository = fmt.Sprintf("%s/%s/%s", strings.TrimRight(reg, "/"), ns, app.Name)
		}

	case models.CheckoutRepository:
		g, err := h.gitRepos.FindInWorkspace(run.WorkspaceID, *def.GitRepositoryID)
		if err != nil {
			return runners.JobInputs{}, fmt.Errorf("pipeline repository %d: %w", *def.GitRepositoryID, err)
		}
		su, err := gitrepo.CredentialURL(g.URL, g, h.secrets)
		if err != nil {
			return runners.JobInputs{}, err
		}
		in.SourceURL = su
		// No app means no shared cache generation; a repository-bound build is uncached until the definition has one.
		if reg != "" {
			// The pl_ prefix keeps a pipeline and an unrelated same-named app from sharing an image repository.
			in.Repository = fmt.Sprintf("%s/%s/%s", strings.TrimRight(reg, "/"), ns, pipelineImageName(def.Name))
		}
	}
	return in, nil
}

// pipelineImagePrefix keeps a pipeline's images out of the application namespace.
const pipelineImagePrefix = "pl_"

func pipelineImageName(name string) string { return pipelineImagePrefix + name }

// An empty result is a command-only pipeline; SSH-key credentials aren't supported for runner builds.
func (h *PipelineHandler) sourceURL(app *models.Application) (string, error) {
	rawURL := app.GitRepo
	var gr *models.GitRepository
	if app.GitRepositoryID != nil {
		g, err := h.gitRepos.FindInWorkspace(app.WorkspaceID, *app.GitRepositoryID)
		if err != nil {
			return "", fmt.Errorf("git credential %d: %w", *app.GitRepositoryID, err)
		}
		gr = g
		if rawURL == "" {
			rawURL = g.URL
		}
	}
	if rawURL == "" {
		return "", nil // command-only pipeline (no git-backed app)
	}
	return gitrepo.CredentialURL(rawURL, gr, h.secrets)
}

type PipelineDeployer struct {
	apps        *repositories.ApplicationRepository
	deployments *repositories.DeploymentRepository
	producer    *Producer
}

func NewPipelineDeployer(apps *repositories.ApplicationRepository, deployments *repositories.DeploymentRepository, producer *Producer) *PipelineDeployer {
	return &PipelineDeployer{apps: apps, deployments: deployments, producer: producer}
}

// The deploy worker pulls an internal-registry ref without rebuilding.
func (d *PipelineDeployer) DeployByDigest(run *models.PipelineRun, appID uint, imageRef string) error {
	app, err := d.apps.FindByID(appID)
	if err != nil {
		return fmt.Errorf("application %d not found: %w", appID, err)
	}
	if app.WorkspaceID != run.WorkspaceID {
		return fmt.Errorf("application does not belong to this workspace")
	}
	dep := &models.Deployment{
		ApplicationID: app.ID,
		Status:        models.DeploymentPending,
		Trigger:       "pipeline",
		Image:         imageRef,
		ImageID:       run.ImageID,
		ImageTag:      run.ImageTag,
		PipelineRunID: &run.ID,
		RunnerID:      run.RunnerID,
		Commit:        run.Commit,
		// Use the app's configured strategy so a pipeline deploy behaves like any other trigger.
		Strategy: models.EffectiveDeployStrategy(app, ""),
	}
	if err := d.deployments.Create(dep); err != nil {
		return fmt.Errorf("create deployment: %w", err)
	}
	return d.producer.EnqueueDeploy(dep.ID, app.ServerID)
}

// subjectUser attributes a run's minted job credentials to whoever triggered it.
func subjectUser(run *models.PipelineRun) uint {
	if run.TriggeredByUserID != nil {
		return *run.TriggeredByUserID
	}
	return 0
}

func (h *PipelineHandler) failRun(run *models.PipelineRun, err error) error {
	end := time.Now()
	run.Status = models.PipelineRunFailed
	run.Error = err.Error()
	run.FinishedAt = &end
	_ = h.pipelines.UpdateRun(run)
	h.log(run.ID, "✖ "+err.Error())
	if h.versions != nil {
		h.versions.Settle(context.Background(), run, "", "", func(line string) { h.log(run.ID, line) })
	}
	h.publishStatus(run.ID, models.PipelineRunFailed)
	// The failure is recorded on the run; asynq must not retry since runs are not idempotent.
	return nil
}

func (h *PipelineHandler) log(runID uint, line string) {
	if h.bus != nil {
		h.bus.Publish(PipelineTopic(runID), eventbus.Event{Type: "log", Data: line})
	}
}

func (h *PipelineHandler) publishStatus(runID uint, status models.PipelineRunStatus) {
	if h.bus == nil {
		return
	}
	h.bus.Publish(PipelineTopic(runID), eventbus.Event{Type: "status", Data: string(status)})
	h.publishRun(runID, status)
}

// Best-effort: a lookup failure costs a live update, never the run.
func (h *PipelineHandler) publishRun(runID uint, status models.PipelineRunStatus) {
	run, err := h.pipelines.FindRunByID(runID)
	if err != nil || run == nil {
		return
	}
	ev := RunEvent{RunID: run.ID, PipelineID: run.PipelineID, Number: run.Number, Status: string(status)}
	if run.StartedAt != nil {
		ev.StartedAt = run.StartedAt.UTC().Format(time.RFC3339)
	}
	if run.FinishedAt != nil {
		ev.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339)
	}
	h.bus.Publish(PipelineWorkspaceTopic(run.WorkspaceID), eventbus.Event{Type: "run", Data: ev})
}
