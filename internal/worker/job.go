// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// JobHandler runs one-off Jobs: a command executed once in an application's
// runtime context (same image/env/networks/volumes/node/limits as the app),
// capturing the exit code and logs. It reuses the deploy pipeline's runtime
// substrate so a Job's environment can't drift from the real deploy.
type JobHandler struct {
	*runtimeBuilder
	jobs       *repositories.JobRepository
	apps       *repositories.ApplicationRepository
	registries *repositories.RegistryRepository
	clients    NodeDocker
	logs       *logstore.Store
}

// SetLogStore wires the shared execution-log store. When set, a job's full
// output is externalized to the store on terminal state and the DB row keeps
// only a bounded tail + a reference. nil keeps DB-tail-only.
func (h *JobHandler) SetLogStore(s *logstore.Store) { h.logs = s }

func NewJobHandler(jobs *repositories.JobRepository, apps *repositories.ApplicationRepository, stackEnv *repositories.StackEnvVarRepository, routeRepo *repositories.RouteRepository, registries *repositories.RegistryRepository, clients NodeDocker, secrets SecretResolver) *JobHandler {
	return &JobHandler{runtimeBuilder: &runtimeBuilder{stackEnv: stackEnv, routes: routeRepo, secrets: secrets}, jobs: jobs, apps: apps, registries: registries, clients: clients}
}

// ProcessTask implements asynq.Handler for the run-job task.
func (h *JobHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p RunJobPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad job payload: %w", err)
	}
	j, err := h.jobs.FindByID(p.JobID)
	if err != nil {
		return fmt.Errorf("job %d not found: %w", p.JobID, err)
	}
	if j.Status.IsTerminal() {
		return nil // already canceled/processed
	}
	h.run(ctx, j)
	return nil
}

func (h *JobHandler) run(ctx context.Context, j *models.Job) {
	app, err := h.apps.FindByID(j.ApplicationID)
	if err != nil {
		h.fail(j, fmt.Errorf("application %d not found: %w", j.ApplicationID, err))
		return
	}
	dc, err := h.clients.For(j.ServerID)
	if err != nil {
		h.fail(j, fmt.Errorf("node is offline: %w", err))
		return
	}

	rc, err := h.buildRuntimeContext(ctx, dc, app)
	if err != nil {
		h.fail(j, fmt.Errorf("prepare runtime: %w", err))
		return
	}

	// A custom image must be pulled (it may be private/absent on the node); the
	// app's active-release image is already present from its deploy.
	if j.Pull {
		auth, aerr := h.registryAuth(app.WorkspaceID, j.RegistryID)
		if aerr != nil {
			h.fail(j, aerr)
			return
		}
		if perr := dc.PullImage(ctx, j.Image, auth); perr != nil {
			h.fail(j, fmt.Errorf("pull image %s: %w", j.Image, perr))
			return
		}
	}

	// Deterministic name doubles as the cancel handle (Cancel removes by it).
	name := fmt.Sprintf("mb-job-%d", j.ID)
	now := time.Now()
	j.Status = models.JobRunning
	j.StartedAt = &now
	j.ContainerID = name
	_ = h.jobs.Update(j)

	runCtx := ctx
	if j.TimeoutSecs > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(j.TimeoutSecs)*time.Second)
		defer cancel()
	}

	// Restricted security profile applies to one-off jobs too: run as the
	// platform non-root UID and chown the app's managed volumes to it first.
	sec := h.containerSecurity(app)
	if sec.Restricted() {
		if err := h.prepareRestrictedVolumes(runCtx, dc, sec, j.Image, rc.Mounts); err != nil {
			h.finish(j, models.JobFailed, nil, fmt.Sprintf("prepare volumes: %v", err))
			return
		}
	}
	spec := docker.RunSpec{
		Name:       name,
		Image:      j.Image,
		Entrypoint: j.Entrypoint,
		Cmd:        j.Command,
		Env:        rc.Env,
		Networks:   rc.Networks,
		Mounts:     rc.Mounts,
		// Volumes were seeded+chowned by prepareRestrictedVolumes under the
		// restricted profile; skip copy-up so it isn't re-owned from the image.
		NoCopyVolumes: sec.Restricted(),
		Binds:         rc.Binds,
		MemoryBytes:   rc.MemoryBytes,
		NanoCPUs:      rc.NanoCPUs,
		Labels: map[string]string{
			docker.LabelApp:       fmt.Sprintf("%d", app.ID),
			docker.LabelJob:       fmt.Sprintf("%d", j.ID),
			docker.LabelWorkspace: fmt.Sprintf("%d", app.WorkspaceID),
		},
	}
	sec.applyTo(&spec)
	exit, out, runErr := dc.RunOneShot(runCtx, spec)

	// Persist captured output regardless of outcome.
	if out != "" {
		_ = h.jobs.AppendLog(j.ID, out)
	}

	// A concurrent Cancel may have force-removed the container and marked the job
	// canceled; if so, respect that terminal state.
	if cur, err := h.jobs.FindByID(j.ID); err == nil && cur.Status == models.JobCanceled {
		h.finish(cur, models.JobCanceled, nil, "canceled")
		return
	}

	fin := time.Now()
	j.FinishedAt = &fin
	switch {
	case runErr != nil:
		h.finish(j, models.JobFailed, &exit, runErr.Error())
	case exit != 0:
		h.finish(j, models.JobFailed, &exit, fmt.Sprintf("command exited with code %d", exit))
	default:
		h.finish(j, models.JobSucceeded, &exit, "")
	}
}

// registryAuth resolves pull credentials for a custom image. nil registryID
// (or no match) means an anonymous pull.
func (h *JobHandler) registryAuth(workspaceID uint, registryID *uint) (*docker.RegistryAuth, error) {
	if registryID == nil || h.registries == nil {
		return nil, nil
	}
	reg, err := h.registries.FindInWorkspace(workspaceID, *registryID)
	if err != nil {
		return nil, fmt.Errorf("registry %d: %w", *registryID, err)
	}
	secret, err := crypto.Decrypt(reg.Secret)
	if err != nil {
		return nil, fmt.Errorf("decrypt registry secret: %w", err)
	}
	return &docker.RegistryAuth{Server: reg.Server, Username: reg.Username, Password: secret}, nil
}

// finish writes a job's terminal state.
func (h *JobHandler) finish(j *models.Job, status models.JobStatus, exit *int, errMsg string) {
	j.Status = status
	if exit != nil {
		j.ExitCode = exit
	}
	if j.FinishedAt == nil {
		now := time.Now()
		j.FinishedAt = &now
	}
	j.Error = errMsg
	if err := h.jobs.Update(j); err != nil {
		logger.Error("failed to record job result", "job", j.ID, "error", err)
	}
	h.externalizeLog(j)
}

// externalizeLog moves a terminal job's full output into the shared log store
// and trims the row to a bounded tail + a reference. No-op when the store is
// disabled or already externalized; on any error the full log stays in the DB
// tail. It re-reads the row so it captures the output AppendLog accumulated.
func (h *JobHandler) externalizeLog(j *models.Job) {
	if !h.logs.Enabled() {
		return
	}
	cur, err := h.jobs.FindByID(j.ID)
	if err != nil || cur.LogRef != "" {
		return
	}
	ref := logstore.JobRef(cur.WorkspaceID, cur.ID)
	res, err := h.logs.Externalize(ref, cur.Logs)
	if err != nil {
		logger.Error("log store: externalize job log failed", "job", cur.ID, "error", err)
		return
	}
	if err := h.jobs.SetLogMeta(cur.ID, res.Ref, res.Tail, res.Bytes, res.Lines, res.Truncated); err != nil {
		logger.Error("log store: record job log ref failed", "job", cur.ID, "error", err)
	}
}

func (h *JobHandler) fail(j *models.Job, cause error) {
	now := time.Now()
	j.FinishedAt = &now
	h.finish(j, models.JobFailed, nil, cause.Error())
	logger.Error("job failed", "job", j.ID, "error", cause)
}
