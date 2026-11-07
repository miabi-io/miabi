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

// WorkspaceQuotaRepository persists per-workspace quota overrides.
type WorkspaceQuotaRepository struct {
	db *gorm.DB
}

func NewWorkspaceQuotaRepository(db *gorm.DB) *WorkspaceQuotaRepository {
	return &WorkspaceQuotaRepository{db: db}
}

func (r *WorkspaceQuotaRepository) FindByWorkspace(workspaceID uint) (*models.WorkspaceQuota, error) {
	var q models.WorkspaceQuota
	if err := r.db.Where("workspace_id = ?", workspaceID).First(&q).Error; err != nil {
		return nil, err
	}
	return &q, nil
}

// Upsert creates or replaces a workspace's override row.
func (r *WorkspaceQuotaRepository) Upsert(q *models.WorkspaceQuota) error {
	return r.db.Save(q).Error
}

// Delete clears a workspace's override (falls back to the plan).
func (r *WorkspaceQuotaRepository) Delete(workspaceID uint) error {
	return r.db.Where("workspace_id = ?", workspaceID).Delete(&models.WorkspaceQuota{}).Error
}
