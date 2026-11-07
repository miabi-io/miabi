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

type RunnerLeaseRepository struct {
	db *gorm.DB
}

func NewRunnerLeaseRepository(db *gorm.DB) *RunnerLeaseRepository {
	return &RunnerLeaseRepository{db: db}
}

func (r *RunnerLeaseRepository) Create(l *models.RunnerLease) error { return r.db.Create(l).Error }
func (r *RunnerLeaseRepository) Update(l *models.RunnerLease) error { return r.db.Save(l).Error }

func (r *RunnerLeaseRepository) ActiveCountByRunner(runnerID uint) (int, error) {
	var n int64
	err := r.db.Model(&models.RunnerLease{}).
		Where("runner_id = ? AND status = ?", runnerID, models.LeaseActive).Count(&n).Error
	return int(n), err
}

func (r *RunnerLeaseRepository) ActiveCountsByRunner() (map[uint]int, error) {
	var rows []struct {
		RunnerID uint
		N        int
	}
	err := r.db.Model(&models.RunnerLease{}).
		Select("runner_id, COUNT(*) AS n").
		Where("status = ?", models.LeaseActive).
		Group("runner_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uint]int, len(rows))
	for _, row := range rows {
		out[row.RunnerID] = row.N
	}
	return out, nil
}

func (r *RunnerLeaseRepository) ActiveByRun(kind models.LeaseKind, runID uint) (*models.RunnerLease, error) {
	var l models.RunnerLease
	if err := r.db.Where("kind = ? AND run_id = ? AND status = ?", kind, runID, models.LeaseActive).First(&l).Error; err != nil {
		return nil, err
	}
	return &l, nil
}

// Release marks a job's active lease done (called when the job goes terminal).
func (r *RunnerLeaseRepository) Release(kind models.LeaseKind, runID uint) error {
	return r.db.Model(&models.RunnerLease{}).
		Where("kind = ? AND run_id = ? AND status = ?", kind, runID, models.LeaseActive).
		Update("status", models.LeaseDone).Error
}

// Runs in one UPDATE to avoid a lost-update race between the sweeper and a late-completing runner.
func (r *RunnerLeaseRepository) ExpireDue(now time.Time) ([]models.RunnerLease, error) {
	var due []models.RunnerLease
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Where("status = ? AND expires_at < ?", models.LeaseActive, now).Find(&due).Error; e != nil {
			return e
		}
		if len(due) == 0 {
			return nil
		}
		ids := make([]uint, len(due))
		for i := range due {
			ids[i] = due[i].ID
		}
		return tx.Model(&models.RunnerLease{}).Where("id IN ?", ids).Update("status", models.LeaseExpired).Error
	})
	return due, err
}
