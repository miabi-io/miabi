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

// CustomRoleRepository accesses admin-defined roles (Enterprise; empty in CE).
type CustomRoleRepository struct{ db *gorm.DB }

func NewCustomRoleRepository(db *gorm.DB) *CustomRoleRepository {
	return &CustomRoleRepository{db: db}
}

func (r *CustomRoleRepository) Create(cr *models.CustomRole) error { return r.db.Create(cr).Error }
func (r *CustomRoleRepository) Update(cr *models.CustomRole) error { return r.db.Save(cr).Error }
func (r *CustomRoleRepository) Delete(id uint) error {
	return r.db.Delete(&models.CustomRole{}, id).Error
}

// Org-wide templates have a nil workspace_id.
func (r *CustomRoleRepository) ListByWorkspace(workspaceID uint) ([]models.CustomRole, error) {
	var roles []models.CustomRole
	err := r.db.Where("workspace_id = ? OR workspace_id IS NULL", workspaceID).
		Order("name ASC").Find(&roles).Error
	return roles, err
}

func (r *CustomRoleRepository) FindInWorkspace(workspaceID, id uint) (*models.CustomRole, error) {
	var cr models.CustomRole
	if err := r.db.Where("id = ? AND (workspace_id = ? OR workspace_id IS NULL)", id, workspaceID).
		First(&cr).Error; err != nil {
		return nil, err
	}
	return &cr, nil
}

func (r *CustomRoleRepository) FindByID(id uint) (*models.CustomRole, error) {
	var cr models.CustomRole
	if err := r.db.First(&cr, id).Error; err != nil {
		return nil, err
	}
	return &cr, nil
}

func (r *CustomRoleRepository) CountMembersUsing(roleID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.WorkspaceMember{}).Where("custom_role_id = ?", roleID).Count(&n).Error
	return n, err
}
