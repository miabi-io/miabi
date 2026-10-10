// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// WorkspaceSealingKeyRepository persists per-workspace sealing key pairs.
type WorkspaceSealingKeyRepository struct {
	db *gorm.DB
}

func NewWorkspaceSealingKeyRepository(db *gorm.DB) *WorkspaceSealingKeyRepository {
	return &WorkspaceSealingKeyRepository{db: db}
}

func (r *WorkspaceSealingKeyRepository) Create(k *models.WorkspaceSealingKey) error {
	return r.db.Create(k).Error
}

// FindActive returns the workspace's active sealing key, or ErrRecordNotFound.
func (r *WorkspaceSealingKeyRepository) FindActive(workspaceID uint) (*models.WorkspaceSealingKey, error) {
	var k models.WorkspaceSealingKey
	if err := r.db.Where("workspace_id = ? AND active = ?", workspaceID, true).First(&k).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

// ListByWorkspace returns every retained version, newest first.
func (r *WorkspaceSealingKeyRepository) ListByWorkspace(workspaceID uint) ([]models.WorkspaceSealingKey, error) {
	var ks []models.WorkspaceSealingKey
	err := r.db.Where("workspace_id = ?", workspaceID).Order("version DESC").Find(&ks).Error
	return ks, err
}

// MaxVersion returns the highest version for a workspace (0 when none).
func (r *WorkspaceSealingKeyRepository) MaxVersion(workspaceID uint) (int, error) {
	var max *int
	if err := r.db.Model(&models.WorkspaceSealingKey{}).
		Where("workspace_id = ?", workspaceID).
		Select("MAX(version)").Scan(&max).Error; err != nil {
		return 0, err
	}
	if max == nil {
		return 0, nil
	}
	return *max, nil
}

// Activate makes k the workspace's only active version and inserts it, in one transaction.
func (r *WorkspaceSealingKeyRepository) Activate(k *models.WorkspaceSealingKey) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.WorkspaceSealingKey{}).
			Where("workspace_id = ?", k.WorkspaceID).Update("active", false).Error; err != nil {
			return err
		}
		k.Active = true
		return tx.Create(k).Error
	})
}

// DeleteByWorkspace removes all of a workspace's sealing keys (crypto-shred on delete).
func (r *WorkspaceSealingKeyRepository) DeleteByWorkspace(workspaceID uint) error {
	return r.db.Where("workspace_id = ?", workspaceID).Delete(&models.WorkspaceSealingKey{}).Error
}

// DB exposes the underlying handle.
func (r *WorkspaceSealingKeyRepository) DB() *gorm.DB { return r.db }
