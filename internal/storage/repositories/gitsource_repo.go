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

// GitSourceRepository persists GitOps source bindings.
type GitSourceRepository struct {
	db *gorm.DB
}

func NewGitSourceRepository(db *gorm.DB) *GitSourceRepository { return &GitSourceRepository{db: db} }

func (r *GitSourceRepository) Create(g *models.GitSource) error { return r.db.Create(g).Error }
func (r *GitSourceRepository) Update(g *models.GitSource) error { return r.db.Save(g).Error }
func (r *GitSourceRepository) Delete(workspaceID, id uint) error {
	return r.db.Where("workspace_id = ?", workspaceID).Delete(&models.GitSource{}, id).Error
}

func (r *GitSourceRepository) FindInWorkspace(workspaceID, id uint) (*models.GitSource, error) {
	var g models.GitSource
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&g).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

// NameInWorkspace returns a project's name, for labelling the resources it manages.
func (r *GitSourceRepository) NameInWorkspace(workspaceID, id uint) (string, error) {
	var name string
	err := r.db.Model(&models.GitSource{}).Select("name").
		Where("id = ? AND workspace_id = ?", id, workspaceID).Limit(1).Scan(&name).Error
	return name, err
}

// FindByID looks a source up without workspace scoping (used by the worker/cron
// sweep, which iterates across workspaces).
func (r *GitSourceRepository) FindByID(id uint) (*models.GitSource, error) {
	var g models.GitSource
	if err := r.db.First(&g, id).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

func (r *GitSourceRepository) ListByWorkspace(workspaceID uint) ([]models.GitSource, error) {
	var out []models.GitSource
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&out).Error
	return out, err
}

// ListAuto returns every source set to automatic sync, across all workspaces —
// the working set for the polling reconciler.
func (r *GitSourceRepository) ListAuto() ([]models.GitSource, error) {
	var out []models.GitSource
	err := r.db.Where("sync_policy = ?", models.GitSyncAuto).Find(&out).Error
	return out, err
}

func (r *GitSourceRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.GitSource{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

// IDByUID resolves a git source's uid to its numeric id.
func (r *GitSourceRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.GitSource](r.db, uid)
}
