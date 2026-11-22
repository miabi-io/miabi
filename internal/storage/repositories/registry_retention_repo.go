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
	"errors"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type RegistryRetentionRepository struct{ db *gorm.DB }

func NewRegistryRetentionRepository(db *gorm.DB) *RegistryRetentionRepository {
	return &RegistryRetentionRepository{db: db}
}

// Policy returns the stored policy, or the defaults when none was saved yet.
func (r *RegistryRetentionRepository) Policy() (*models.RegistryRetention, error) {
	var p models.RegistryRetention
	err := r.db.First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		d := models.DefaultRegistryRetention()
		return &d, nil
	}
	return &p, err
}

// Select("*") so a deliberate 0 is written rather than skipped.
func (r *RegistryRetentionRepository) SavePolicy(p *models.RegistryRetention) error {
	p.ID = 1
	return r.db.Clauses(clause.OnConflict{UpdateAll: true}).Select("*").Create(p).Error
}

func (r *RegistryRetentionRepository) Overrides() (map[uint]models.RegistryRetentionOverride, error) {
	var rows []models.RegistryRetentionOverride
	if err := r.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]models.RegistryRetentionOverride, len(rows))
	for _, o := range rows {
		out[o.WorkspaceID] = o
	}
	return out, nil
}

// Override is the workspace's row, or a zero one that follows the platform.
func (r *RegistryRetentionRepository) Override(workspaceID uint) (*models.RegistryRetentionOverride, error) {
	o := models.RegistryRetentionOverride{WorkspaceID: workspaceID}
	err := r.db.Where("workspace_id = ?", workspaceID).First(&o).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return &o, nil
}

func (r *RegistryRetentionRepository) SaveOverride(o *models.RegistryRetentionOverride) error {
	return r.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(o).Error
}

// StartRun refuses a second run while one is going, so a schedule and a button never overlap.
func (r *RegistryRetentionRepository) StartRun(run *models.RegistryRetentionRun, staleAfter time.Duration) (bool, error) {
	started := false
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var running int64
		if err := tx.Model(&models.RegistryRetentionRun{}).
			Where("status = ? AND started_at > ?", models.RetentionRunRunning, time.Now().Add(-staleAfter)).
			Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			return nil
		}
		started = true
		return tx.Create(run).Error
	})
	return started, err
}

func (r *RegistryRetentionRepository) FinishRun(run *models.RegistryRetentionRun) error {
	return r.db.Save(run).Error
}

func (r *RegistryRetentionRepository) Runs(limit int) ([]models.RegistryRetentionRun, error) {
	var out []models.RegistryRetentionRun
	err := r.db.Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

func (r *RegistryRetentionRepository) Run(id uint) (*models.RegistryRetentionRun, error) {
	var run models.RegistryRetentionRun
	if err := r.db.First(&run, id).Error; err != nil {
		return nil, err
	}
	return &run, nil
}
