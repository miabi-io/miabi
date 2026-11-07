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

type ResourcePolicyRepository struct{ db *gorm.DB }

func NewResourcePolicyRepository(db *gorm.DB) *ResourcePolicyRepository {
	return &ResourcePolicyRepository{db: db}
}

// Find returns the grant for (workspace, user, resource), or gorm.ErrRecordNotFound.
func (r *ResourcePolicyRepository) Find(workspaceID, userID uint, resourceType string, resourceID uint) (*models.ResourcePolicy, error) {
	var p models.ResourcePolicy
	err := r.db.Where("workspace_id = ? AND user_id = ? AND resource_type = ? AND resource_id = ?",
		workspaceID, userID, resourceType, resourceID).First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *ResourcePolicyRepository) HasPermission(workspaceID, userID uint, resourceType string, resourceID uint, perm models.Permission) bool {
	p, err := r.Find(workspaceID, userID, resourceType, resourceID)
	if err != nil {
		return false
	}
	return p.Grants(perm)
}

// Upsert creates or replaces the grant for (workspace, user, resource).
func (r *ResourcePolicyRepository) Upsert(p *models.ResourcePolicy) error {
	existing, err := r.Find(p.WorkspaceID, p.UserID, p.ResourceType, p.ResourceID)
	if err == nil {
		existing.Permissions = p.Permissions
		return r.db.Save(existing).Error
	}
	return r.db.Create(p).Error
}

// ListByResource returns all grants on a resource (with the granted users).
func (r *ResourcePolicyRepository) ListByResource(workspaceID uint, resourceType string, resourceID uint) ([]models.ResourcePolicy, error) {
	var policies []models.ResourcePolicy
	err := r.db.Preload("User").
		Where("workspace_id = ? AND resource_type = ? AND resource_id = ?", workspaceID, resourceType, resourceID).
		Order("created_at ASC").Find(&policies).Error
	return policies, err
}

// DeleteForUser revokes a user's grant on a resource.
func (r *ResourcePolicyRepository) DeleteForUser(workspaceID, userID uint, resourceType string, resourceID uint) error {
	return r.db.Where("workspace_id = ? AND user_id = ? AND resource_type = ? AND resource_id = ?",
		workspaceID, userID, resourceType, resourceID).Delete(&models.ResourcePolicy{}).Error
}

// DeleteForResource removes every grant on a resource (cascade on resource delete).
func (r *ResourcePolicyRepository) DeleteForResource(workspaceID uint, resourceType string, resourceID uint) error {
	return r.db.Where("workspace_id = ? AND resource_type = ? AND resource_id = ?",
		workspaceID, resourceType, resourceID).Delete(&models.ResourcePolicy{}).Error
}
