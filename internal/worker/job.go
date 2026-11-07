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
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// JobHandler reuses the deploy runtime substrate so a Job's environment can't drift from the real deploy.
type JobHandler struct {
	*runtimeBuilder
	jobs        *repositories.JobRepository
	apps        *repositories.ApplicationRepository
	registries  *repositories.RegistryRepository
	clients     NodeDocker
	logs        *logstore.Store
	deployments *repositories.DeploymentRepository
	releases    *repositories.ReleaseRepository
	waitPoll    time.Duration
	waitMax     time.Duration
	// onFailed hears about a declared Job's failed run, to retry it.
	onFailed func(jobID uint)
}

// SetFailureHook wires what a declared Job's failed run reports to (backoffLimit retries).
func (h *JobHandler) SetFailureHook(fn func(jobID uint)) { h.onFailed = fn }

// Without it a run held for a deploy starts straight away on its enqueued image.
func (h *JobHandler) SetDeployWait(deployments *repositories.DeploymentRepository, releases *repositories.ReleaseRepository) {
	h.deployments, h.releases = deployments, releases
	h.waitPoll, h.waitMax = 5*time.Second, 2*time.Hour
}

// When set, full output goes to the store on terminal state and the DB row keeps a bounded tail.
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
	if j.WaitDeploymentID != nil && h.deployments != nil {
		if !h.awaitDeploy(ctx, j) {
			return
		}
	}
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

	// The active-release image is already on the node; a custom one may be private or absent.
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

	ownImage := strings.TrimSpace(j.Image) != "" && !strings.EqualFold(strings.TrimSpace(j.Image), strings.TrimSpace(app.Image))
	sec, secErr := h.workloadSecurityFor(app, j.RunAsUser, app.OfficialTemplate && !ownImage)
	if secErr != nil {
		h.finish(j, models.JobFailed, nil, secErr.Error())
		return
	}
	// A job may write into the image (migrations, asset builds); only the app's own containers run read-only.
	sec.ReadOnlyRootfs = false
	if sec.HasUser() {
		if err := h.prepareVolumeOwnership(runCtx, dc, sec, j.Image, rc.Mounts); err != nil {
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
		// Volumes are already chowned for the pinned user; copy-up would re-own them from the image.
		NoCopyVolumes: sec.HasUser(),
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

	// A concurrent Cancel may already have removed the container and marked the job canceled.
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

func (h *JobHandler) registryAuth(workspaceID uint, registryID *uint) (*docker.RegistryAuth, error) {
	if registryID == nil || h.registries == nil {
		return nil, nil
	}
	reg, err := h.registries.FindInWorkspace(workspaceID, *registryID)
	if err != nil {
		return nil, fmt.Errorf("registry %d: %w", *registryID, err)
	}
	password, err := h.credentialSecret(workspaceID, reg.Secret, reg.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("registry %q: %w", reg.Name, err)
	}
	return &docker.RegistryAuth{Server: reg.Server, Username: reg.Username, Password: password}, nil
}

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
	if status == models.JobFailed && j.JobDefinitionID != nil && h.onFailed != nil {
		h.onFailed(j.ID)
	}
}

// Re-reads the row to capture the output AppendLog accumulated; on any error the full log stays in the DB.
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

// A failed deploy skips the run, and a cancel while waiting ends it.
func (h *JobHandler) awaitDeploy(ctx context.Context, j *models.Job) bool {
	deadline := time.Now().Add(h.waitMax)
	for {
		dep, err := h.deployments.FindByID(*j.WaitDeploymentID)
		if err != nil {
			break // gone: nothing left to wait for
		}
		if dep.Status == models.DeploymentFailed {
			h.finish(j, models.JobSkipped, nil, fmt.Sprintf("deployment #%d failed; run skipped", dep.Number))
			return false
		}
		// A canary leaves the stable release active, which is what the run uses.
		if dep.Status.IsTerminal() || dep.Status == models.DeploymentCanary {
			break
		}
		if time.Now().After(deadline) {
			h.finish(j, models.JobSkipped, nil, fmt.Sprintf("deployment #%d still running after %s; run skipped", dep.Number, h.waitMax))
			return false
		}
		select {
		case <-ctx.Done():
			h.fail(j, fmt.Errorf("worker stopped while waiting for deployment #%d", dep.Number))
			return false
		case <-time.After(h.waitPoll):
		}
		if cur, err := h.jobs.FindByID(j.ID); err == nil && cur.Status.IsTerminal() {
			return false // canceled while waiting
		}
	}
	if !j.Pull && h.releases != nil {
		rel, err := h.releases.FindActive(j.ApplicationID)
		if err != nil || rel.Image == "" {
			if j.Image == "" {
				h.fail(j, fmt.Errorf("application has no active release to run on"))
				return false
			}
			return true
		}
		j.Image = rel.Image
	}
	return true
}

func (h *JobHandler) fail(j *models.Job, cause error) {
	now := time.Now()
	j.FinishedAt = &now
	h.finish(j, models.JobFailed, nil, cause.Error())
	logger.Error("job failed", "job", j.ID, "error", cause)
}
