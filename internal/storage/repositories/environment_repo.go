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

// A nil environment counts workspace-level approvals.
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
