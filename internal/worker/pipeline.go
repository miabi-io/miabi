// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// PipelineHandler is the internal runner: it clones the source once into a per-run workspace, then
// executes the run's steps in order over that shared filesystem — container steps in one-shot
// containers, plus the built-in `build` and `deploy` steps. Remote runners lease the same steps.
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

	// Runner dispatch (wired in the process that holds the runner tunnels). Every
	// pipeline build runs on a registered runner; there is no on-node fallback.
	dispatcher        RunnerDispatcher
	workspaces        *repositories.WorkspaceRepository
	registry          string               // fallback registry host (raw MIABI_REGISTRY_HOST)
	registryHosts     RegistryHostResolver // resolves the live host (UI-/domain-derived)
	runnerWaitTimeout time.Duration        // how long a run waits for a runner before failing
}

// RegistryHostResolver resolves the live registry host a runner logs into and
// pushes to. It tracks a UI-set or domain-derived host rather than a static env
// value, so the login host and push host stay identical (a mismatch → "denied").
type RegistryHostResolver interface{ RegistryHost() string }

const builderHandoffTimeout = 10 * time.Minute

// registryHost is the effective host runners use: the live resolved host when
// available, else the raw env fallback.
func (h *PipelineHandler) registryHost() string {
	if h.registryHosts != nil {
		if host := h.registryHosts.RegistryHost(); host != "" {
			return host
		}
	}
	return h.registry
}

// RunnerDispatcher runs a pipeline on a dedicated runner over the machine API.
// Implemented by runners.Dispatcher. Kept as an interface so the handler stays
// testable.
type RunnerDispatcher interface {
	// Dispatch runs the pipeline on a runner and drives it to a terminal status,
	// or returns runners.ErrNoRunner / ErrRunnerOffline when none can take it now.
	Dispatch(ctx context.Context, in runners.JobInputs, requiredLabels []string, subjectUserID uint) error
}

// SetRunnerDispatch wires runner dispatch: every pipeline build runs on a
// registered runner, and a run with no available runner waits (up to
// runnerWaitTimeout) rather than building on a node.
func (h *PipelineHandler) SetRunnerDispatch(d RunnerDispatcher, workspaces *repositories.WorkspaceRepository, registry string, hosts RegistryHostResolver, runnerWaitTimeout time.Duration) {
	h.dispatcher = d
	h.workspaces = workspaces
	h.registry = registry
	h.registryHosts = hosts
	h.runnerWaitTimeout = runnerWaitTimeout
}

// SetLogStore wires the shared execution-log store. When set, a pipeline step's
// full log is externalized to the store on terminal state and the DB row keeps
// only a bounded tail + a reference. nil keeps DB-tail-only.
func (h *PipelineHandler) SetLogStore(s *logstore.Store) { h.logs = s }

// SetSecrets wires the vault, so a Git credential that references a workspace
// Secret (rather than storing its own copy of the token) can be resolved when
// the runner clones. nil = literal credentials only.
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

	// Every build runs on a registered runner — there is no on-node fallback. This worker may simply not
	// be the process holding the runner tunnels (a standalone `miabi worker`), which is a routing problem:
	// hand the run to the control-plane worker. Checked before the run is marked running.
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

// runOnRunner dispatches the run to a runner and drives it to completion. When no
// runner can take it right now (none registered, or all busy/offline) the run
// waits and is retried, up to runnerWaitTimeout.
func (h *PipelineHandler) runOnRunner(ctx context.Context, run *models.PipelineRun, def *models.PipelineDefinition) error {
	in, err := h.jobInputs(run, def)
	if err != nil {
		return h.failRun(run, err)
	}
	err = h.dispatcher.Dispatch(ctx, in, nil, subjectUser(run))
	switch {
	case err == nil:
		h.markCacheBuilt(run, in)
		return nil // Dispatch drove the run to a terminal status
	case errors.Is(err, runnersvc.ErrNoRunner), errors.Is(err, runners.ErrRunnerOffline):
		return h.waitForRunner(run) // none available right now — wait (bounded)
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

// waitForRunner parks a run back in pending and re-enqueues it shortly, so it never builds on a node
// while it waits. If no runner has become available within runnerWaitTimeout, measured from when the
// run was created, the run fails rather than waiting forever — pointing the user at Runners.
func (h *PipelineHandler) waitForRunner(run *models.PipelineRun) error {
	if h.runnerWaitTimeout > 0 && time.Since(run.CreatedAt) > h.runnerWaitTimeout {
		return h.failRun(run, fmt.Errorf(
			"no runner became available within %s — register a runner (Settings → Runners)", h.runnerWaitTimeout))
	}
	run.Status = models.PipelineRunPending
	run.StartedAt = nil
	_ = h.pipelines.UpdateRun(run)
	h.log(run.ID, "waiting for an available runner…")
	h.publishStatus(run.ID, models.PipelineRunPending)
	return h.producer.EnqueuePipelineRunIn(run.ID, runnerWaitInterval)
}

func (h *PipelineHandler) jobInputs(run *models.PipelineRun, def *models.PipelineDefinition) (runners.JobInputs, error) {
	// The runner logs into Registry and pushes to Repository, so both must carry
	// the same resolved host (login-host ≠ push-host → "denied").
	reg := h.registryHost()
	var in runners.JobInputs
	// The push namespace is the immutable ws_<id> form, not the workspace name a user types. Both
	// authorize identically, but the pushed reference is recorded on the deployment and re-pulled later
	// — a rename in between would leave it pointing at nothing. The name is still the docker-login user.
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
		if app, err := h.apps.FindByID(*def.ApplicationID); err == nil {
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
				// Push under <host>/ws_<id>/<app-name> so the deploy path recognizes it as a build ref, and the
				// ownership check on the pull resolves the namespace back to this workspace.
				in.Repository = fmt.Sprintf("%s/%s/%s", strings.TrimRight(reg, "/"), ns, app.Name)
			}
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
		// No app means no shared cache generation to key off; a repository-bound
		// build is uncached until the definition carries its own generation.
		if reg != "" {
			// Namespaced apart from application images (pl_<name>) so a pipeline and
			// an unrelated app of the same name can never push into the same image
			// repository. The first path segment still resolves to this workspace, so
			// registry authorization is unchanged.
			in.Repository = fmt.Sprintf("%s/%s/%s", strings.TrimRight(reg, "/"), ns, pipelineImageName(def.Name))
		}
	}
	return in, nil
}

// pipelineImagePrefix keeps a pipeline's images out of the application namespace.
const pipelineImagePrefix = "pl_"

// pipelineImageName is the repository path a repository-bound pipeline pushes to,
// within the workspace namespace.
func pipelineImageName(name string) string { return pipelineImagePrefix + name }

// sourceURL resolves the app's git clone URL for the runner, embedding the linked HTTPS credential so
// a private repo clones on the runner. An empty result (no app URL) is a command-only pipeline;
// SSH-key credentials aren't supported for runner builds.
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

// PipelineDeployer performs the deploy-by-digest a runner build triggers: it creates a deployment of
// the pushed image and enqueues it to the app's node, which pulls and runs it. Implements
// runners.Deployer.
type PipelineDeployer struct {
	apps        *repositories.ApplicationRepository
	deployments *repositories.DeploymentRepository
	producer    *Producer
}

func NewPipelineDeployer(apps *repositories.ApplicationRepository, deployments *repositories.DeploymentRepository, producer *Producer) *PipelineDeployer {
	return &PipelineDeployer{apps: apps, deployments: deployments, producer: producer}
}

// DeployByDigest creates and enqueues a deploy of imageRef (repo@digest) for the
// run's app. The deploy worker recognizes an internal-registry ref and pulls it
// (no rebuild), pinned to the run's commit and image for provenance.
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
		RunnerID:      run.RunnerID,
		Commit:        run.Commit,
		// The app's configured strategy, not the column default. Leaving it unset meant an app set to
		// canary in its settings rolled straight out when the deploy came from a pipeline — the same
		// app, the same setting, a different answer depending on what triggered it.
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
	h.publishStatus(run.ID, models.PipelineRunFailed)
	// Returning nil: the failure is recorded on the run; asynq must not retry
	// (runs are not idempotent and MaxRetry is 0 anyway).
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
