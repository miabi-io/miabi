// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/services/database"
)

// ProvisionDBHandler runs database provisioning tasks.
type ProvisionDBHandler struct {
	dbs *database.Service
}

func NewProvisionDBHandler(dbs *database.Service) *ProvisionDBHandler {
	return &ProvisionDBHandler{dbs: dbs}
}

// ProcessTask implements asynq.Handler for the database-provision task.
func (h *ProvisionDBHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p ProvisionDBPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad provision payload: %w", err)
	}
	if p.Resize {
		return h.dbs.RunResize(ctx, p.DatabaseID, database.Resources{MemoryBytes: p.PrevMemoryBytes, NanoCPUs: p.PrevNanoCPUs, Size: p.PrevSize})
	}
	if err := h.dbs.RunProvision(ctx, p.DatabaseID); err != nil {
		logger.Error("database provisioning failed", "database", p.DatabaseID, "error", err)
		return err
	}
	return nil
}

// UpgradeDBHandler runs database version-upgrade tasks.
type UpgradeDBHandler struct {
	dbs *database.Service
}

func NewUpgradeDBHandler(dbs *database.Service) *UpgradeDBHandler {
	return &UpgradeDBHandler{dbs: dbs}
}

// ProcessTask implements asynq.Handler for the database-upgrade task. The job
// records its own success/failure on the instance, so it returns nil (no retry)
// except on an undecodable payload.
func (h *UpgradeDBHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p UpgradeDBPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad upgrade payload: %w", err)
	}
	return h.dbs.RunUpgradeJob(ctx, p.DatabaseID, p.Target, p.Path, p.StopApps)
}
