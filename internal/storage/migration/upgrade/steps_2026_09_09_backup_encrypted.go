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

// The ".gpg" suffix the *-bkup tools append is the record of encryption; the column makes it queryable.
func backupEncryptedStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasColumn(&backupRow{}, "encrypted") {
		return nil
	}
	err := db.WithContext(ctx).Exec(
		`UPDATE backups SET encrypted = true WHERE encrypted = false AND filename LIKE '%.gpg'`).Error
	if err != nil {
		return fmt.Errorf("backfill backups.encrypted: %w", err)
	}
	return nil
}

// backupRow names the table for the migrator without importing models.
type backupRow struct{}

func (backupRow) TableName() string { return "backups" }

func init() {
	steps = append(steps, Step{
		Name:    "backup_encrypted_backfill",
		Version: "1.9.6",
		Run:     backupEncryptedStep,
	})
}
