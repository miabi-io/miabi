// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// backupEncryptedStep marks the backups already taken under a passphrase. The
// ".gpg" suffix is what the *-bkup tools append when they encrypt, so the stored
// filename is the record of what happened — the column just makes it queryable.
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
