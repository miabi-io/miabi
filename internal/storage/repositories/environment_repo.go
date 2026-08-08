// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// EnvironmentRepository persists promotion environments and release approvals.
type EnvironmentRepository struct {
	db *gorm.DB
}

func NewEnvironmentRepository(db *gorm.DB) *EnvironmentRepository {
	return &EnvironmentRepository{db: db}
}

func (r *EnvironmentRepository) Create(e *models.Environment) error { return r.db.Create(e).Error }
func (r *EnvironmentRepository) Update(e *models.Environment) error { return r.db.Save(e).Error }
func (r *EnvironmentRepository) Delete(workspaceID, id uint) error {
	return r.db.Where("workspace_id = ?", workspaceID).Delete(&models.Environment{}, id).Error
}

func (r *EnvironmentRepository) FindInWorkspace(workspaceID, id uint) (*models.Environment, error) {
	var e models.Environment
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&e).Error; err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *EnvironmentRepository) ListByWorkspace(workspaceID uint) ([]models.Environment, error) {
	var out []models.Environment
	err := r.db.Where("workspace_id = ?", workspaceID).Order("rank ASC, created_at ASC").Find(&out).Error
	return out, err
}

func (r *EnvironmentRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Environment{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

func (r *EnvironmentRepository) CreateApproval(a *models.ReleaseApproval) error {
	return r.db.Create(a).Error
}

// CountApprovals returns how many positive approvals a release has for an
// environment (env nil counts workspace-level approvals).
func (r *EnvironmentRepository) CountApprovals(releaseID uint, environmentID *uint) (int64, error) {
	q := r.db.Model(&models.ReleaseApproval{}).
		Where("release_id = ? AND approved = ?", releaseID, true)
	if environmentID != nil {
		q = q.Where("environment_id = ?", *environmentID)
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

func (r *EnvironmentRepository) ListApprovals(releaseID uint) ([]models.ReleaseApproval, error) {
	var out []models.ReleaseApproval
	err := r.db.Where("release_id = ?", releaseID).Order("created_at ASC").Find(&out).Error
	return out, err
}
