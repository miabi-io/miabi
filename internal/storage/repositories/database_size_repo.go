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

type DatabaseSizeRepository struct {
	db *gorm.DB
}

func NewDatabaseSizeRepository(db *gorm.DB) *DatabaseSizeRepository {
	return &DatabaseSizeRepository{db: db}
}

// List returns every size, smallest first.
func (r *DatabaseSizeRepository) List() ([]models.DatabaseSize, error) {
	var out []models.DatabaseSize
	err := r.db.Order("memory_bytes ASC, nano_cpus ASC, name ASC").Find(&out).Error
	return out, err
}

func (r *DatabaseSizeRepository) FindByID(id uint) (*models.DatabaseSize, error) {
	var s models.DatabaseSize
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *DatabaseSizeRepository) FindByName(name string) (*models.DatabaseSize, error) {
	var s models.DatabaseSize
	if err := r.db.Where("name = ?", name).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *DatabaseSizeRepository) Create(s *models.DatabaseSize) error { return r.db.Create(s).Error }

func (r *DatabaseSizeRepository) Update(s *models.DatabaseSize) error { return r.db.Save(s).Error }

func (r *DatabaseSizeRepository) Delete(id uint) error {
	return r.db.Delete(&models.DatabaseSize{}, id).Error
}

// The lists are JSON columns, so they're matched here rather than in SQL.
func (r *DatabaseSizeRepository) OfferedBy(id uint) (plans []string, workspaceIDs []uint, err error) {
	var ps []models.Plan
	if err := r.db.Select("id", "name", "database_sizes").Find(&ps).Error; err != nil {
		return nil, nil, err
	}
	for _, p := range ps {
		if slices.Contains(p.DatabaseSizes, id) {
			plans = append(plans, p.Name)
		}
	}
	var qs []models.WorkspaceQuota
	if err := r.db.Select("workspace_id", "database_sizes").Find(&qs).Error; err != nil {
		return nil, nil, err
	}
	for _, q := range qs {
		if q.DatabaseSizes != nil && slices.Contains(*q.DatabaseSizes, id) {
			workspaceIDs = append(workspaceIDs, q.WorkspaceID)
		}
	}
	return plans, workspaceIDs, nil
}
