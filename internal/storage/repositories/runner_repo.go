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
	"slices"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// RunnerRepository persists runners; platform-shared runners have a nil WorkspaceID.
type RunnerRepository struct {
	db *gorm.DB
}

func NewRunnerRepository(db *gorm.DB) *RunnerRepository { return &RunnerRepository{db: db} }

func (r *RunnerRepository) Create(m *models.Runner) error { return r.db.Create(m).Error }
func (r *RunnerRepository) Update(m *models.Runner) error { return r.db.Save(m).Error }

func (r *RunnerRepository) Delete(id uint) error {
	return r.db.Delete(&models.Runner{}, id).Error
}

// FindByID resolves a runner by primary key (any scope).
func (r *RunnerRepository) FindByID(id uint) (*models.Runner, error) {
	var m models.Runner
	if err := r.db.First(&m, id).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *RunnerRepository) FindInWorkspace(workspaceID, id uint) (*models.Runner, error) {
	var m models.Runner
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindShared resolves a platform-shared runner (nil workspace) by id.
func (r *RunnerRepository) FindShared(id uint) (*models.Runner, error) {
	var m models.Runner
	if err := r.db.Where("id = ? AND workspace_id IS NULL", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindByTokenHash resolves a runner by its registration-token hash (any scope).
func (r *RunnerRepository) FindByTokenHash(hash string) (*models.Runner, error) {
	var m models.Runner
	if err := r.db.Where("token_hash = ?", hash).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// ListByWorkspace returns a workspace's own runners, newest first.
func (r *RunnerRepository) ListByWorkspace(workspaceID uint) ([]models.Runner, error) {
	var out []models.Runner
	err := r.db.Where("workspace_id = ?", workspaceID).Order("id DESC").Find(&out).Error
	return out, err
}

// ListShared returns all platform-shared runners (nil workspace), newest first.
func (r *RunnerRepository) ListShared() ([]models.Runner, error) {
	var out []models.Runner
	err := r.db.Where("workspace_id IS NULL").Order("id DESC").Find(&out).Error
	return out, err
}

// Plans bind shared runners by name because it is the stable handle and survives a re-register.
func (r *RunnerRepository) FindSharedByName(name string) (*models.Runner, error) {
	var m models.Runner
	if err := r.db.Where("workspace_id IS NULL AND name = ?", name).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// BoundBy lets removal of a still-bound runner be refused; the lists are JSON, so membership is matched in Go.
func (r *RunnerRepository) BoundBy(name string) ([]models.Plan, error) {
	var plans []models.Plan
	if err := r.db.Find(&plans).Error; err != nil {
		return nil, err
	}
	var out []models.Plan
	for _, p := range plans {
		if slices.Contains(p.PlatformRunners, name) {
			out = append(out, p)
		}
	}
	return out, nil
}

// CountShared reports the size of the platform-shared pool, for the edition cap.
func (r *RunnerRepository) CountShared() (int64, error) {
	var count int64
	err := r.db.Model(&models.Runner{}).Where("workspace_id IS NULL").Count(&count).Error
	return count, err
}

func (r *RunnerRepository) ListAll() ([]models.Runner, error) {
	var out []models.Runner
	err := r.db.Order("id DESC").Find(&out).Error
	return out, err
}

func (r *RunnerRepository) ListSchedulable(workspaceID uint, includeShared bool) ([]models.Runner, error) {
	var out []models.Runner
	q := r.db.Where("workspace_id = ?", workspaceID)
	if includeShared {
		q = r.db.Where("workspace_id = ? OR workspace_id IS NULL", workspaceID)
	}
	err := q.Order("id DESC").Find(&out).Error
	return out, err
}

func (r *RunnerRepository) ExistsByName(workspaceID *uint, name string, excludeID uint) (bool, error) {
	q := r.db.Model(&models.Runner{}).Where("name = ? AND id <> ?", name, excludeID)
	if workspaceID == nil {
		q = q.Where("workspace_id IS NULL")
	} else {
		q = q.Where("workspace_id = ?", *workspaceID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
