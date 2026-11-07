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

package upgrade

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

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
