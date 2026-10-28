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

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type WorkspaceBackupSettingsRepository struct {
	db *gorm.DB
}

func NewWorkspaceBackupSettingsRepository(db *gorm.DB) *WorkspaceBackupSettingsRepository {
	return &WorkspaceBackupSettingsRepository{db: db}
}

// FindByWorkspace returns the settings for a workspace, or gorm.ErrRecordNotFound
// when none have been saved yet.
func (r *WorkspaceBackupSettingsRepository) FindByWorkspace(workspaceID uint) (*models.WorkspaceBackupSettings, error) {
	var s models.WorkspaceBackupSettings
	if err := r.db.Where("workspace_id = ?", workspaceID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// backupSettingsColumns are the columns an upsert reassigns — EVERY writable column of the model.
// A field missing from this list is written by the first insert and silently ignored by every
// update after it. Adding a settings field means adding it here; a test asserts they stay in step.
var backupSettingsColumns = []string{
	"s3_enabled", "s3_endpoint", "s3_bucket", "s3_region", "s3_access_key",
	"s3_secret_key_enc", "s3_use_ssl", "s3_force_path_style",
	"database_backup_path", "volume_backup_path",
	"bundle_path", "bundle_passphrase_enc", "backup_passphrase_enc",
	"backup_encryption", "age_recipients", "age_identity_enc", "skip_backup_verify", "updated_at",
}

// Upsert inserts or updates the workspace's settings (keyed by workspace_id).
func (r *WorkspaceBackupSettingsRepository) Upsert(s *models.WorkspaceBackupSettings) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "workspace_id"}},
		DoUpdates: clause.AssignmentColumns(backupSettingsColumns),
	}).Create(s).Error
}
