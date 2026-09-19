// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

func clusterDedicationStep(ctx context.Context, db *gorm.DB) error {
	n, err := repositories.NewClusterRepository(db.WithContext(ctx)).ClearUnusableWorkspaceDefaults()
	if err != nil {
		return err
	}
	if n > 0 {
		logger.Info("cleared workspace default locations their organization can no longer place in", "workspaces", n)
	}
	return nil
}

func init() {
	steps = append(steps, Step{
		Name:    "cluster_dedication_defaults",
		Version: "1.10.4",
		Run:     clusterDedicationStep,
	})
}
