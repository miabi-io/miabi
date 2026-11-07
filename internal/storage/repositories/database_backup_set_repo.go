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

type DatabaseBackupSetRepository struct{ db *gorm.DB }

func NewDatabaseBackupSetRepository(db *gorm.DB) *DatabaseBackupSetRepository {
	return &DatabaseBackupSetRepository{db: db}
}

func (r *DatabaseBackupSetRepository) Create(s *models.DatabaseBackupSet) error {
	return r.db.Create(s).Error
}

func (r *DatabaseBackupSetRepository) Update(s *models.DatabaseBackupSet) error {
	return r.db.Save(s).Error
}

// Scoped so a set id from another tenant reads as not found.
func (r *DatabaseBackupSetRepository) FindInWorkspace(workspaceID, id uint) (*models.DatabaseBackupSet, error) {
	var s models.DatabaseBackupSet
	err := r.db.Preload("Items", func(db *gorm.DB) *gorm.DB {
		return db.Order("backups.created_at DESC")
	}).Where("workspace_id = ? AND id = ?", workspaceID, id).First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindByRef loads a set by its stable name, with items.
func (r *DatabaseBackupSetRepository) FindByRef(workspaceID uint, ref string) (*models.DatabaseBackupSet, error) {
	var s models.DatabaseBackupSet
	err := r.db.Preload("Items").Where("workspace_id = ? AND ref = ?", workspaceID, ref).First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Loads items in one query pair rather than N+1.
func (r *DatabaseBackupSetRepository) ListByInstance(instanceID uint) ([]models.DatabaseBackupSet, error) {
	var sets []models.DatabaseBackupSet
	err := r.db.Preload("Items", func(db *gorm.DB) *gorm.DB {
		return db.Order("backups.id ASC")
	}).Where("instance_id = ?", instanceID).Order("created_at DESC").Find(&sets).Error
	return sets, err
}

// Items cascade in the database; the service removes their artifacts first, which the database can't.
func (r *DatabaseBackupSetRepository) Delete(id uint) error {
	return r.db.Delete(&models.DatabaseBackupSet{}, id).Error
}

func (r *DatabaseBackupSetRepository) FindByID(id uint) (*models.DatabaseBackupSet, error) {
	var s models.DatabaseBackupSet
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *DatabaseBackupSetRepository) ListSealed(workspaceID uint) ([]models.DatabaseBackupSet, error) {
	var out []models.DatabaseBackupSet
	err := r.db.Where("workspace_id = ? AND envelope <> ''", workspaceID).Find(&out).Error
	return out, err
}

// CreateSchedule stores a new instance-level set schedule.
func (r *DatabaseBackupSetRepository) CreateSchedule(s *models.DatabaseBackupSetSchedule) error {
	return r.db.Create(s).Error
}

func (r *DatabaseBackupSetRepository) UpdateSchedule(s *models.DatabaseBackupSetSchedule) error {
	return r.db.Save(s).Error
}

func (r *DatabaseBackupSetRepository) DeleteSchedule(id uint) error {
	return r.db.Delete(&models.DatabaseBackupSetSchedule{}, id).Error
}

// Scoped so a schedule id from another tenant reads as not found.
func (r *DatabaseBackupSetRepository) FindScheduleInWorkspace(workspaceID, id uint) (*models.DatabaseBackupSetSchedule, error) {
	var s models.DatabaseBackupSetSchedule
	err := r.db.Where("workspace_id = ? AND id = ?", workspaceID, id).First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *DatabaseBackupSetRepository) ListSchedulesByInstance(instanceID uint) ([]models.DatabaseBackupSetSchedule, error) {
	var out []models.DatabaseBackupSetSchedule
	err := r.db.Where("instance_id = ?", instanceID).Order("id ASC").Find(&out).Error
	return out, err
}

// ListEnabledSchedules feeds the cron manager at startup.
func (r *DatabaseBackupSetRepository) ListEnabledSchedules() ([]models.DatabaseBackupSetSchedule, error) {
	var out []models.DatabaseBackupSetSchedule
	err := r.db.Where("enabled = ?", true).Find(&out).Error
	return out, err
}

func (r *DatabaseBackupSetRepository) ListAllRefs() (map[string]uint, error) {
	var rows []models.DatabaseBackupSet
	if err := r.db.Select("id", "ref").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]uint, len(rows))
	for i := range rows {
		out[rows[i].Ref] = rows[i].ID
	}
	return out, nil
}
