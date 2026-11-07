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
	"fmt"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// WorkspaceKeyRepository persists per-workspace wrapped data-encryption keys.
type WorkspaceKeyRepository struct {
	db *gorm.DB
}

func NewWorkspaceKeyRepository(db *gorm.DB) *WorkspaceKeyRepository {
	return &WorkspaceKeyRepository{db: db}
}

func (r *WorkspaceKeyRepository) Create(k *models.WorkspaceKey) error { return r.db.Create(k).Error }
func (r *WorkspaceKeyRepository) Update(k *models.WorkspaceKey) error { return r.db.Save(k).Error }

// FindActive returns the workspace's active key version, or ErrRecordNotFound.
func (r *WorkspaceKeyRepository) FindActive(workspaceID uint) (*models.WorkspaceKey, error) {
	var k models.WorkspaceKey
	if err := r.db.Where("workspace_id = ? AND active = ?", workspaceID, true).First(&k).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

// FindVersion returns a specific key version (for decrypting old ciphertext).
func (r *WorkspaceKeyRepository) FindVersion(workspaceID uint, version int) (*models.WorkspaceKey, error) {
	var k models.WorkspaceKey
	if err := r.db.Where("workspace_id = ? AND version = ?", workspaceID, version).First(&k).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

// ListByWorkspace returns all key versions for a workspace (newest first).
func (r *WorkspaceKeyRepository) ListByWorkspace(workspaceID uint) ([]models.WorkspaceKey, error) {
	var ks []models.WorkspaceKey
	err := r.db.Where("workspace_id = ?", workspaceID).Order("version DESC").Find(&ks).Error
	return ks, err
}

// MaxVersion returns the highest version number for a workspace (0 when none).
func (r *WorkspaceKeyRepository) MaxVersion(workspaceID uint) (int, error) {
	var max *int
	if err := r.db.Model(&models.WorkspaceKey{}).
		Where("workspace_id = ?", workspaceID).
		Select("MAX(version)").Scan(&max).Error; err != nil {
		return 0, err
	}
	if max == nil {
		return 0, nil
	}
	return *max, nil
}

func (r *WorkspaceKeyRepository) DeactivateAll(tx *gorm.DB, workspaceID uint) error {
	db := r.db
	if tx != nil {
		db = tx
	}
	return db.Model(&models.WorkspaceKey{}).
		Where("workspace_id = ?", workspaceID).Update("active", false).Error
}

func (r *WorkspaceKeyRepository) ListActiveOlderThan(cutoff time.Time) ([]models.WorkspaceKey, error) {
	var ks []models.WorkspaceKey
	err := r.db.Where("active = ? AND rotated_at < ?", true, cutoff).Find(&ks).Error
	return ks, err
}

func (r *WorkspaceKeyRepository) DeleteOldVersions(workspaceID uint, keep int) error {
	return r.db.Where("workspace_id = ? AND version <> ?", workspaceID, keep).
		Delete(&models.WorkspaceKey{}).Error
}

// Scans every text and JSON column to catch unregistered owners; slow on large tables, but rotation is rare.
func (r *WorkspaceKeyRepository) StaleCiphertext(workspaceID uint, activeVersion int) ([]string, error) {
	var cols []struct {
		TableName  string
		ColumnName string
	}
	err := r.db.Raw(`SELECT c.table_name, c.column_name
		FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = current_schema() AND t.table_type = 'BASE TABLE'
		  AND c.data_type IN ('text', 'character varying', 'json', 'jsonb')
		ORDER BY c.table_name, c.column_name`).Scan(&cols).Error
	if err != nil {
		return nil, err
	}
	pattern := fmt.Sprintf(`e2:w:%d:(?!%d:)[0-9]+:`, workspaceID, activeVersion)
	var stale []string
	for _, c := range cols {
		var found bool
		q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE %s::text ~ ?)`, quoteIdent(c.TableName), quoteIdent(c.ColumnName))
		if err := r.db.Raw(q, pattern).Scan(&found).Error; err != nil {
			return nil, fmt.Errorf("scan %s.%s: %w", c.TableName, c.ColumnName, err)
		}
		if found {
			stale = append(stale, c.TableName+"."+c.ColumnName)
		}
	}
	return stale, nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// DeleteByWorkspace removes all of a workspace's keys (crypto-shred on delete).
func (r *WorkspaceKeyRepository) DeleteByWorkspace(workspaceID uint) error {
	return r.db.Where("workspace_id = ?", workspaceID).Delete(&models.WorkspaceKey{}).Error
}

// DB exposes the underlying handle for transactional rotation.
func (r *WorkspaceKeyRepository) DB() *gorm.DB { return r.db }
