// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

// WorkspaceBundleRunner executes one bundle run. Satisfied by services/wsbackup.Service and named here
// as an interface rather than imported: that service drives the application service, which enqueues
// deploys through this package, and a direct import would close the loop.
type WorkspaceBundleRunner interface {
	Run(ctx context.Context, bundleID uint) error
}

// WorkspaceBundleHandler runs portable workspace bundle exports and restores.
type WorkspaceBundleHandler struct {
	svc WorkspaceBundleRunner
}

func NewWorkspaceBundleHandler(svc WorkspaceBundleRunner) *WorkspaceBundleHandler {
	return &WorkspaceBundleHandler{svc: svc}
}

// ProcessTask implements asynq.Handler for the workspace-bundle task.
func (h *WorkspaceBundleHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p WorkspaceBundlePayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad workspace bundle payload: %w", err)
	}
	if err := h.svc.Run(ctx, p.WorkspaceBundleID); err != nil {
		logger.Error("workspace bundle run failed", "bundle", p.WorkspaceBundleID, "error", err)
		return err
	}
	return nil
}
