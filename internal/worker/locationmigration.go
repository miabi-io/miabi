// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
)

type LocationMigrationRunner interface {
	Run(ctx context.Context, migrationID uint) error
}

// LocationMigrationHandler runs location migrations.
type LocationMigrationHandler struct {
	svc LocationMigrationRunner
}

func NewLocationMigrationHandler(svc LocationMigrationRunner) *LocationMigrationHandler {
	return &LocationMigrationHandler{svc: svc}
}

// ProcessTask implements asynq.Handler for the location-migration task.
func (h *LocationMigrationHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p LocationMigrationPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad location migration payload: %w", err)
	}
	return h.svc.Run(ctx, p.MigrationID)
}
