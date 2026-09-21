// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// workspaceLimitSentinelKeys are the two per-user workspace limits that used to read "0 or less means
// unlimited", against the -1 convention every other limit in the platform uses.
var workspaceLimitSentinelKeys = []string{"max_workspaces_per_user", "max_workspace_memberships_per_user"}

// workspaceLimitSentinelStep rewrites a stored 0 (or lower) to -1 so it keeps meaning "unlimited"
// under the single convention now applied everywhere: -1 unlimited, 0 none, N = N.
//
// Without this an install that had deliberately set 0 to lift the cap would wake up with the cap set
// to "no workspaces at all" — the exact opposite of what its operator asked for.
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
