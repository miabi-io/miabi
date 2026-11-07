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

package handlers

import (
	"strconv"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/worker"
)

const (
	// maxDeployWait bounds ?wait= on deploy and rollback.
	maxDeployWait = 15 * time.Minute
	// The event bus is in-process, so a deploy run by a separate worker process publishes nothing here.
	waitPoll = 2 * time.Second
	// waitHeader is set to "timeout" when ?wait= ran out before the deploy settled.
	waitHeader = "X-Miabi-Wait"
)

func waitDuration(secs int) time.Duration {
	d := time.Duration(secs) * time.Second
	if d > maxDeployWait {
		return maxDeployWait
	}
	return d
}

// A canary counts as settled: it waits for a human to promote or abort it.
func deploySettled(s models.DeploymentStatus) bool {
	return s.IsTerminal() || s == models.DeploymentCanary
}

func (h *ApplicationHandler) waitDeployment(c *okapi.Context, dep *models.Deployment, secs int) (*models.Deployment, bool) {
	events, unsubscribe := h.bus.Subscribe(worker.DeployTopic(dep.ID))
	defer unsubscribe()
	deadline := time.NewTimer(waitDuration(secs))
	defer deadline.Stop()
	tick := time.NewTicker(waitPoll)
	defer tick.Stop()
	ctx := c.Request().Context()
	for {
		if fresh, err := h.svc.GetDeployment(dep.ID); err == nil {
			dep = fresh
		}
		if deploySettled(dep.Status) {
			return dep, true
		}
		select {
		case <-ctx.Done():
			return dep, false
		case <-deadline.C:
			return dep, false
		case <-events:
		case <-tick.C:
		}
	}
}

// waitPipelineRun is waitDeployment for an app whose deploys run its pipeline.
func (h *ApplicationHandler) waitPipelineRun(c *okapi.Context, workspaceID uint, run *models.PipelineRun, secs int) (*models.PipelineRun, bool) {
	events, unsubscribe := h.bus.Subscribe(worker.PipelineTopic(run.ID))
	defer unsubscribe()
	deadline := time.NewTimer(waitDuration(secs))
	defer deadline.Stop()
	tick := time.NewTicker(waitPoll)
	defer tick.Stop()
	ctx := c.Request().Context()
	for {
		if fresh, err := h.svc.GetPipelineRun(workspaceID, run.ID); err == nil {
			run = fresh
		}
		if run.Status.IsTerminal() {
			return run, true
		}
		select {
		case <-ctx.Done():
			return run, false
		case <-deadline.C:
			return run, false
		case <-events:
		case <-tick.C:
		}
	}
}

// GetDeployment returns one deployment of the app.
func (h *ApplicationHandler) GetDeployment(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	id, err := strconv.Atoi(c.Param("deploymentID"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid deployment id")
	}
	dep, err := h.svc.GetDeployment(uint(id))
	if err != nil || dep.ApplicationID != app.ID {
		return c.AbortNotFound("deployment not found")
	}
	return ok(c, dep)
}
