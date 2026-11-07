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

type MetricRepository struct {
	db *gorm.DB
}

func NewMetricRepository(db *gorm.DB) *MetricRepository { return &MetricRepository{db: db} }

func (r *MetricRepository) Insert(m *models.MetricSample) error { return r.db.Create(m).Error }

// ListByApp returns samples for an app recorded since `since`, oldest first.
func (r *MetricRepository) ListByApp(appID uint, since time.Time, limit int) ([]models.MetricSample, error) {
	var samples []models.MetricSample
	q := r.db.Where("application_id = ? AND recorded_at >= ?", appID, since).Order("recorded_at ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&samples).Error
	return samples, err
}

func (r *MetricRepository) ListByApps(appIDs []uint, since time.Time) ([]models.MetricSample, error) {
	if len(appIDs) == 0 {
		return nil, nil
	}
	var samples []models.MetricSample
	err := r.db.Where("application_id IN ? AND recorded_at >= ?", appIDs, since).
		Order("recorded_at ASC").Find(&samples).Error
	return samples, err
}

// Prune deletes samples older than `before`.
func (r *MetricRepository) Prune(before time.Time) (int64, error) {
	res := r.db.Where("recorded_at < ?", before).Delete(&models.MetricSample{})
	return res.RowsAffected, res.Error
}
