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
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type PlanRepository struct {
	db *gorm.DB
}

func NewPlanRepository(db *gorm.DB) *PlanRepository { return &PlanRepository{db: db} }

// GORM swaps zero values for column defaults on insert, so non-zero-default columns are written back explicitly.
func (r *PlanRepository) Create(p *models.Plan) error {
	allowService, dbCPU, dbMem := p.AllowServiceRuntime, p.MaxDatabaseCPUCores, p.MaxDatabaseMemoryMB
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(p).Error; err != nil {
			return err
		}
		p.AllowServiceRuntime, p.MaxDatabaseCPUCores, p.MaxDatabaseMemoryMB = allowService, dbCPU, dbMem
		return tx.Model(&models.Plan{}).Where("id = ?", p.ID).UpdateColumns(map[string]any{
			"allow_service_runtime": allowService, "max_database_cpu_cores": dbCPU, "max_database_memory_mb": dbMem,
		}).Error
	})
}
func (r *PlanRepository) Update(p *models.Plan) error { return r.db.Save(p).Error }

func (r *PlanRepository) Count() (int64, error) {
	var n int64
	err := r.db.Model(&models.Plan{}).Count(&n).Error
	return n, err
}

func (r *PlanRepository) FindByID(id uint) (*models.Plan, error) {
	var p models.Plan
	if err := r.db.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// FindByName returns a plan by its exact name.
func (r *PlanRepository) FindByName(name string) (*models.Plan, error) {
	var p models.Plan
	if err := r.db.Where("name = ?", name).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// FindDefault returns the active default plan, if one is configured.
func (r *PlanRepository) FindDefault() (*models.Plan, error) {
	var p models.Plan
	if err := r.db.Where("is_default = ? AND is_active = ?", true, true).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *PlanRepository) ListPaged(search string, limit, offset int) ([]models.Plan, int64, error) {
	var (
		plans []models.Plan
		total int64
	)
	q := r.db.Model(&models.Plan{})
	if s := strings.TrimSpace(search); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	q.Count(&total)
	if err := q.Order("name ASC").Limit(limit).Offset(offset).Find(&plans).Error; err != nil {
		return nil, 0, err
	}
	return plans, total, nil
}

func (r *PlanRepository) Delete(id uint) error {
	return r.db.Delete(&models.Plan{}, id).Error
}

// ClearDefault unsets is_default on every plan; call it in a transaction before marking a new default.
func (r *PlanRepository) ClearDefault(tx *gorm.DB) error {
	if tx == nil {
		tx = r.db
	}
	return tx.Model(&models.Plan{}).Where("is_default = ?", true).Update("is_default", false).Error
}

// CountWorkspaces reports how many workspaces are assigned to a plan.
func (r *PlanRepository) CountWorkspaces(planID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Workspace{}).Where("plan_id = ?", planID).Count(&n).Error
	return n, err
}

// UnassignAll clears plan_id from every workspace pointing at this plan.
func (r *PlanRepository) UnassignAll(planID uint) error {
	return r.db.Model(&models.Workspace{}).Where("plan_id = ?", planID).Update("plan_id", nil).Error
}

// AssignToWorkspace sets a workspace's plan (planID nil clears it).
func (r *PlanRepository) AssignToWorkspace(workspaceID uint, planID *uint) error {
	return r.db.Model(&models.Workspace{}).Where("id = ?", workspaceID).Update("plan_id", planID).Error
}

// PlanForWorkspace returns the plan assigned to a workspace, or gorm.ErrRecordNotFound.
func (r *PlanRepository) PlanForWorkspace(workspaceID uint) (*models.Plan, error) {
	var p models.Plan
	err := r.db.Joins("JOIN workspaces ON workspaces.plan_id = plans.id").
		Where("workspaces.id = ?", workspaceID).First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// HasAny reports whether any plan exists (used to seed defaults once).
func (r *PlanRepository) HasAny() (bool, error) {
	var n int64
	if err := r.db.Model(&models.Plan{}).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
