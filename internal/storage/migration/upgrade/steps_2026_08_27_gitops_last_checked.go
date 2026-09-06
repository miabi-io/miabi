// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

func gitopsLastCheckedStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasColumn(&gitSourceRow{}, "last_checked_at") {
		return nil
	}
	err := db.WithContext(ctx).Exec(`UPDATE git_sources
		SET last_checked_at = last_synced_at, last_checked_commit = last_synced_commit
		WHERE last_checked_at IS NULL AND last_synced_at IS NOT NULL`).Error
	if err != nil {
		return fmt.Errorf("seed git_sources.last_checked_at: %w", err)
	}
	return nil
}

// gitSourceRow names the table for the migrator without importing models.
type gitSourceRow struct{}

func (gitSourceRow) TableName() string { return "git_sources" }

func init() {
	steps = append(steps, Step{
		Name:    "gitops_last_checked_backfill",
		Version: "1.9.0",
		Run:     gitopsLastCheckedStep,
	})
}
