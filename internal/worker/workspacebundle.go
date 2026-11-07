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

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

// An interface rather than an import, because wsbackup enqueues deploys through this package.
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
