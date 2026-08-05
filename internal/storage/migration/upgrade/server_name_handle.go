// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

func serverNameHandleStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasColumn(&serverRow{}, "slug") {
		// A fresh install: AutoMigrate created the table from the current model,
		// so there is no slug column and nothing to move.
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE servers SET display_name = name
			WHERE (display_name IS NULL OR display_name = '')`).Error; err != nil {
			return fmt.Errorf("copy name to display_name: %w", err)
		}
		if err := tx.Exec(`UPDATE servers SET name = slug
			WHERE slug IS NOT NULL AND slug <> ''`).Error; err != nil {
			return fmt.Errorf("copy slug to name: %w", err)
		}
		return nil
	})
}

// serverRow names the servers table for the migrator without importing
// models.Server — the model no longer has a Slug field, so HasColumn must be
// asked about the table, not the struct.
type serverRow struct{}

func (serverRow) TableName() string { return "servers" }

func init() {
	steps = append(steps, Step{
		Name:    "server_name_handle_swap",
		Version: "1.7.1",
		Run:     serverNameHandleStep,
	})
}
