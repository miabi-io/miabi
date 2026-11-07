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

// These used to read "0 or less means unlimited", unlike the -1 convention everywhere else.
var workspaceLimitSentinelKeys = []string{"max_workspaces_per_user", "max_workspace_memberships_per_user"}

// Without this, an install that set 0 to lift the cap would wake up allowing no workspaces at all.
func workspaceLimitSentinelStep(ctx context.Context, db *gorm.DB) error {

	err := db.WithContext(ctx).
		Exec(`UPDATE settings SET value = '-1'
		      WHERE key IN ? AND value <> '-1' AND (trim(value) = '0' OR trim(value) LIKE '-%')`,
			workspaceLimitSentinelKeys).Error
	if err != nil {
		return fmt.Errorf("normalize workspace limit sentinels: %w", err)
	}
	return nil
}

func init() {
	steps = append(steps, Step{
		Name:    "workspace_limit_sentinel",
		Version: "1.10.5",
		Run:     workspaceLimitSentinelStep,
	})
}
