// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// userDefaultWorkspaceStep seeds each existing user's default workspace with the one
// they have belonged to longest.
func userDefaultWorkspaceStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasColumn(&userRow{}, "default_workspace_id") {
		return nil
	}
	err := db.WithContext(ctx).Exec(`UPDATE users SET default_workspace_id = (
			SELECT wm.workspace_id FROM workspace_members wm
			WHERE wm.user_id = users.id
			ORDER BY wm.created_at ASC, wm.workspace_id ASC
			LIMIT 1
		) WHERE default_workspace_id IS NULL`).Error
	if err != nil {
		return fmt.Errorf("seed users.default_workspace_id: %w", err)
	}
	return nil
}

// userRow names the table for the migrator without importing models.
type userRow struct{}

func (userRow) TableName() string { return "users" }

func init() {
	steps = append(steps, Step{
		Name:    "user_default_workspace_seed",
		Version: "1.9.4",
		Run:     userDefaultWorkspaceStep,
	})
}
