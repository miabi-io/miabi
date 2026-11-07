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

type StackRepository struct {
	db *gorm.DB
}

func NewStackRepository(db *gorm.DB) *StackRepository { return &StackRepository{db: db} }

func (r *StackRepository) Create(s *models.Stack) error { return r.db.Create(s).Error }
func (r *StackRepository) Update(s *models.Stack) error { return r.db.Save(s).Error }

// Callers detach or delete member apps first, so no application references the stack here.
func (r *StackRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("stack_id = ?", id).Delete(&models.StackEnvVar{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Stack{}, id).Error
	})
}

func (r *StackRepository) FindInWorkspace(workspaceID, id uint) (*models.Stack, error) {
	var s models.Stack
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *StackRepository) FindInWorkspaceWithApps(workspaceID, id uint) (*models.Stack, error) {
	var s models.Stack
	err := r.db.Preload("Apps", func(db *gorm.DB) *gorm.DB {
		return db.Order("created_at DESC")
	}).Where("id = ? AND workspace_id = ?", id, workspaceID).First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *StackRepository) ListByWorkspace(workspaceID uint) ([]models.Stack, error) {
	var stacks []models.Stack
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&stacks).Error
	return stacks, err
}

// CountByWorkspace returns how many stacks a workspace has.
func (r *StackRepository) CountByWorkspace(workspaceID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Stack{}).Where("workspace_id = ?", workspaceID).Count(&count).Error
	return count, err
}

func (r *StackRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Stack{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

// A soft-deleted stack reads as absent, the housekeeping "orphan" condition.
func (r *StackRepository) ExistsByID(id uint) (bool, error) {
	var count int64
	err := r.db.Model(&models.Stack{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *StackRepository) FindByName(workspaceID uint, name string) (*models.Stack, error) {
	var s models.Stack
	if err := r.db.Where("workspace_id = ? AND name = ?", workspaceID, name).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *StackRepository) ExistsByDockerName(dockerName string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Stack{}).Where("docker_name = ?", dockerName).Count(&count).Error
	return count > 0, err
}

// CountApps returns how many applications belong to a stack.
func (r *StackRepository) CountApps(stackID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Application{}).Where("stack_id = ?", stackID).Count(&count).Error
	return count, err
}

func (r *StackRepository) StatusCounts(stackID uint) (map[models.AppStatus]int, error) {
	var rows []struct {
		Status models.AppStatus
		Count  int
	}
	err := r.db.Model(&models.Application{}).
		Select("status, count(*) as count").
		Where("stack_id = ?", stackID).
		Group("status").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[models.AppStatus]int, len(rows))
	for _, row := range rows {
		out[row.Status] = row.Count
	}
	return out, nil
}

// AppIDs returns the ids of a stack's member applications.
func (r *StackRepository) AppIDs(stackID uint) ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.Application{}).Where("stack_id = ?", stackID).Pluck("id", &ids).Error
	return ids, err
}

// DetachApps orphans a deleted stack's apps so they keep running rather than being destroyed.
func (r *StackRepository) DetachApps(stackID uint) error {
	return r.db.Model(&models.Application{}).
		Where("stack_id = ?", stackID).Update("stack_id", nil).Error
}

// IDByUID resolves a stack's uid to its numeric id.
func (r *StackRepository) IDByUID(uid string) (uint, error) { return idByUID[models.Stack](r.db, uid) }
