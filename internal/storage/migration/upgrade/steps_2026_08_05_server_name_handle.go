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

func serverNameHandleStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasColumn(&serverRow{}, "slug") {
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

// The model no longer has a Slug field, so HasColumn must be asked about the table, not the struct.
type serverRow struct{}

func (serverRow) TableName() string { return "servers" }

func init() {
	steps = append(steps, Step{
		Name:    "server_name_handle_swap",
		Version: "1.7.1",
		Run:     serverNameHandleStep,
	})
}
