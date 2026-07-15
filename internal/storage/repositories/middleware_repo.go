// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type MiddlewareRepository struct {
	db *gorm.DB
}

func NewMiddlewareRepository(db *gorm.DB) *MiddlewareRepository { return &MiddlewareRepository{db: db} }

func (r *MiddlewareRepository) Create(m *models.Middleware) error { return r.db.Create(m).Error }
func (r *MiddlewareRepository) Update(m *models.Middleware) error { return r.db.Save(m).Error }
func (r *MiddlewareRepository) Delete(id uint) error {
	return r.db.Delete(&models.Middleware{}, id).Error
}

func (r *MiddlewareRepository) FindInWorkspace(workspaceID, id uint) (*models.Middleware, error) {
	var m models.Middleware
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *MiddlewareRepository) ListByWorkspace(workspaceID uint) ([]models.Middleware, error) {
	var mws []models.Middleware
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&mws).Error
	return mws, err
}

// ListAll returns every middleware across workspaces. A node's Goma config
// includes all middlewares (routes reference them by name).
func (r *MiddlewareRepository) ListAll() ([]models.Middleware, error) {
	var mws []models.Middleware
	err := r.db.Order("id ASC").Find(&mws).Error
	return mws, err
}

// CountByWorkspace returns how many middlewares a workspace owns. Used to make
// default-seeding idempotent (skip when the workspace already has policies).
func (r *MiddlewareRepository) CountByWorkspace(workspaceID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Middleware{}).Where("workspace_id = ?", workspaceID).Count(&count).Error
	return count, err
}

func (r *MiddlewareRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Middleware{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}
