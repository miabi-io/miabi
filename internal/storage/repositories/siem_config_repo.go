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
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type SIEMConfigRepository struct{ db *gorm.DB }

func NewSIEMConfigRepository(db *gorm.DB) *SIEMConfigRepository {
	return &SIEMConfigRepository{db: db}
}

func (r *SIEMConfigRepository) Create(c *models.SIEMConfig) error { return r.db.Create(c).Error }
func (r *SIEMConfigRepository) Save(c *models.SIEMConfig) error   { return r.db.Save(c).Error }
func (r *SIEMConfigRepository) Delete(id uint) error {
	return r.db.Delete(&models.SIEMConfig{}, id).Error
}

func (r *SIEMConfigRepository) FindByID(id uint) (*models.SIEMConfig, error) {
	var c models.SIEMConfig
	if err := r.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *SIEMConfigRepository) FindAll() ([]models.SIEMConfig, error) {
	var configs []models.SIEMConfig
	err := r.db.Order("created_at ASC").Find(&configs).Error
	return configs, err
}

// FindEnabled returns the active streaming targets the streamer ships to.
func (r *SIEMConfigRepository) FindEnabled() ([]models.SIEMConfig, error) {
	var configs []models.SIEMConfig
	err := r.db.Where("enabled = ?", true).Order("id ASC").Find(&configs).Error
	return configs, err
}

func (r *SIEMConfigRepository) AdvanceCursor(id, lastShippedID uint) error {
	now := time.Now()
	return r.db.Model(&models.SIEMConfig{}).Where("id = ?", id).
		Updates(map[string]any{"last_shipped_id": lastShippedID, "last_shipped_at": now, "last_error": ""}).Error
}

// RecordError stores the last sink failure without advancing the cursor, so events are retried next tick.
func (r *SIEMConfigRepository) RecordError(id uint, msg string) error {
	return r.db.Model(&models.SIEMConfig{}).Where("id = ?", id).Update("last_error", msg).Error
}
