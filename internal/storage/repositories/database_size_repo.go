// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// OfferedBy names the plans, and the workspaces overriding their plan, that offer a size. The lists are JSON
// columns, so they are matched here rather than in SQL.
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
