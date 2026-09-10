// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

var authAccessEnvOnlyKeys = []string{"registration_enabled", "password_reset_enabled"}

func authAccessEnvOnlyStep(ctx context.Context, db *gorm.DB) error {
	err := db.WithContext(ctx).
		Exec(`DELETE FROM settings WHERE key IN ?`, authAccessEnvOnlyKeys).Error
	if err != nil {
		return fmt.Errorf("drop env-only auth settings: %w", err)
	}
	return nil
}

func init() {
	steps = append(steps, Step{
		Name:    "auth_access_env_only",
		Version: "1.9.7",
		Run:     authAccessEnvOnlyStep,
	})
}
