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
	"fmt"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

type PreReleaseSource interface {
	BeforeReleaseDefinitions(appID uint) ([]models.JobDefinition, error)
	SucceededForDeployment(defID, deploymentID uint) bool
	NewBeforeReleaseRun(def *models.JobDefinition, deploymentID uint, image string, attempt int) (*models.Job, error)
}

// preReleaseRunner is the JobHandler side, an interface so the step is testable without Docker.
type preReleaseRunner interface {
	runBeforeRelease(ctx context.Context, j *models.Job, release managedRun)
	result(jobID uint) (*models.Job, error)
}

// preReleaseLogLines bounds how much of a run's output is copied into the deployment log.
const preReleaseLogLines = 200

// SetPreRelease enables beforeRelease Jobs; the runs execute inline on the deploy's goroutine.
func (h *DeployHandler) SetPreRelease(jobs *JobHandler, defs PreReleaseSource) {
	h.preJobs, h.preDefs = jobs, defs
}

func (h *JobHandler) result(jobID uint) (*models.Job, error) { return h.jobs.FindByID(jobID) }

// These deploys put back what already passed its pre-release step, so running it again would only re-run old code.
var preReleaseSkips = map[string]string{
	"rollback":           "rollback",
	"migration-rollback": "rollback",
	"promote":            "canary promotion",
	"start":              "restart of the active release",
	"reconcile":          "recovery of the active release",
	"migration":          "move to another location",
}

// runPreRelease returns false once it has failed the deploy.
func (h *DeployHandler) runPreRelease(ctx context.Context, app *models.Application, dep *models.Deployment, image string) bool {
	if h.preJobs == nil || h.preDefs == nil {
		return true
	}
	// Read here, not at enqueue, so an apply that stored the Job after queuing the deploy is still honoured.
	defs, err := h.preDefs.BeforeReleaseDefinitions(app.ID)
	if err != nil {
		_ = h.fail(dep, fmt.Errorf("list pre-release jobs: %w", err))
		return false
	}
	if len(defs) == 0 {
		return true
	}
	if reason, ok := preReleaseSkips[dep.Trigger]; ok {
		h.log(dep, "pre-release: skipped ("+reason+")")
		return true
	}
	release := h.deployRun(ctx, app, dep, image)
	for i := range defs {
		if !h.runPreReleaseJob(ctx, app, dep, &defs[i], image, release) {
			return false
		}
	}
	return true
}

func (h *DeployHandler) runPreReleaseJob(ctx context.Context, app *models.Application, dep *models.Deployment, def *models.JobDefinition, image string, release managedRun) bool {
	if h.preDefs.SucceededForDeployment(def.ID, dep.ID) {
		h.log(dep, fmt.Sprintf("pre-release: job %q already succeeded for this deployment", def.Name))
		return true
	}
	h.emit(app, dep, models.EventPreReleaseStarted, models.SeverityInfo, fmt.Sprintf("Pre-release job %q started", def.Name))
	attempts := def.BackoffLimit + 1
	cause := "no run started"
	for attempt := 1; attempt <= attempts; attempt++ {
		j, err := h.preDefs.NewBeforeReleaseRun(def, dep.ID, image, attempt)
		if err != nil {
			cause = err.Error()
			break
		}
		h.log(dep, fmt.Sprintf("pre-release: running job %q (attempt %d/%d)", def.Name, attempt, attempts))
		start := time.Now()
		h.preJobs.runBeforeRelease(ctx, j, release)
		done, err := h.preJobs.result(j.ID)
		if err != nil {
			cause = err.Error()
			break
		}
		h.copyJobOutput(dep, def.Name, done.Logs)
		took := time.Since(start).Round(time.Second)
		if done.Status == models.JobSucceeded {
			h.log(dep, fmt.Sprintf("pre-release: job %q succeeded in %s", def.Name, took))
			h.emit(app, dep, models.EventPreReleaseSucceeded, models.SeverityInfo, fmt.Sprintf("Pre-release job %q succeeded", def.Name))
			return true
		}
		cause = done.Error
		if cause == "" {
			cause = string(done.Status)
		}
		h.log(dep, fmt.Sprintf("pre-release: job %q failed after %s: %s", def.Name, took, cause))
		if ctx.Err() != nil {
			break
		}
	}
	h.emit(app, dep, models.EventPreReleaseFailed, models.SeverityError, fmt.Sprintf("Pre-release job %q failed: %s", def.Name, cause))
	_ = h.failBeforeSwap(dep, fmt.Errorf("pre-release job %q failed: %s", def.Name, cause))
	return false
}

func (h *DeployHandler) copyJobOutput(dep *models.Deployment, name, out string) {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return
	}
	lines := strings.Split(out, "\n")
	if len(lines) > preReleaseLogLines {
		h.log(dep, fmt.Sprintf("[%s] … %d earlier lines are in the job's log", name, len(lines)-preReleaseLogLines))
		lines = lines[len(lines)-preReleaseLogLines:]
	}
	prefix := "[" + name + "] "
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	h.logLines(dep.ID, lines)
}
