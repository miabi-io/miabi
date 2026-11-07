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
	"github.com/miabi-io/miabi/internal/services/analytics"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AnalyticsRepository struct {
	db *gorm.DB
}

func NewAnalyticsRepository(db *gorm.DB) *AnalyticsRepository {
	return &AnalyticsRepository{db: db}
}

// Under a row lock so concurrent consumers never lose or double-count a bucket.
func (r *AnalyticsRepository) Upsert(rollups []*models.AnalyticsRollup) error {
	if len(rollups) == 0 {
		return nil
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		for _, in := range rollups {
			var existing models.AnalyticsRollup
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("workspace_id = ? AND application_id = ? AND route_name = ? AND bucket = ?",
					in.WorkspaceID, in.ApplicationID, in.RouteName, in.Bucket).
				First(&existing).Error
			switch {
			case err == gorm.ErrRecordNotFound:
				if err := tx.Create(in).Error; err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				analytics.Merge(&existing, in)
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (r *AnalyticsRepository) Range(workspaceID uint, appID *uint, since, until time.Time) ([]models.AnalyticsRollup, error) {
	q := r.db.Where("workspace_id = ? AND bucket >= ? AND bucket < ?", workspaceID, since, until)
	if appID != nil {
		q = q.Where("application_id = ?", *appID)
	}
	var rows []models.AnalyticsRollup
	err := q.Order("bucket ASC").Find(&rows).Error
	return rows, err
}

// The top-K and histogram JSON blobs dwarf the counters, so naming columns keeps the query small.
var summaryColumns = []string{
	"id", "bucket", "requests", "bytes_in", "bytes_out",
	"status2xx", "status3xx", "status4xx", "status5xx",
	"duration_hist", "duration_sum", "visitors_hll",
}

var summaryGeoColumns = append(append([]string{}, summaryColumns...), "top_countries")

func (r *AnalyticsRepository) RangeSummary(workspaceID uint, appID *uint, since, until time.Time, withGeo bool) ([]models.AnalyticsRollup, error) {
	cols := summaryColumns
	if withGeo {
		cols = summaryGeoColumns
	}
	q := r.db.Model(&models.AnalyticsRollup{}).
		Select(cols).
		Where("workspace_id = ? AND bucket >= ? AND bucket < ?", workspaceID, since, until)
	if appID != nil {
		q = q.Where("application_id = ?", *appID)
	}
	var rows []models.AnalyticsRollup
	err := q.Order("bucket ASC").Find(&rows).Error
	return rows, err
}

func (r *AnalyticsRepository) AppIDs(workspaceID uint, since, until time.Time) ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.AnalyticsRollup{}).
		Where("workspace_id = ? AND bucket >= ? AND bucket < ? AND application_id > 0", workspaceID, since, until).
		Distinct().Pluck("application_id", &ids).Error
	return ids, err
}

func (r *AnalyticsRepository) Prune(before time.Time) (int64, error) {
	res := r.db.Where("bucket < ?", before).Delete(&models.AnalyticsRollup{})
	return res.RowsAffected, res.Error
}
