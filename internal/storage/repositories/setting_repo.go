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
	"gorm.io/gorm/clause"
)

type SettingRepository struct {
	db *gorm.DB
}

func NewSettingRepository(db *gorm.DB) *SettingRepository { return &SettingRepository{db: db} }

// All returns every setting, ordered by key.
func (r *SettingRepository) All() ([]models.Setting, error) {
	var settings []models.Setting
	if err := r.db.Order("key ASC").Find(&settings).Error; err != nil {
		return nil, err
	}
	return settings, nil
}

// Get returns a single setting by key.
func (r *SettingRepository) Get(key string) (*models.Setting, error) {
	var s models.Setting
	if err := r.db.Where("key = ?", key).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SettingRepository) BulkUpsert(settings []models.Setting) error {
	if len(settings) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "type", "updated_at"}),
	}).Create(&settings).Error
}

// CreateIfMissing seeds defaults on first boot without clobbering admin changes.
func (r *SettingRepository) CreateIfMissing(s models.Setting) error {
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&s).Error
}

// Absent keys are not an error, so clearing a marker is idempotent.
func (r *SettingRepository) Delete(key string) error {
	return r.db.Where("key = ?", key).Delete(&models.Setting{}).Error
}
