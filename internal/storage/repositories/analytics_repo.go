// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/analytics"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AnalyticsRepository persists the minute-bucketed rollups the analytics
// consumer produces and answers range queries for the dashboards.
type AnalyticsRepository struct {
	db *gorm.DB
}

func NewAnalyticsRepository(db *gorm.DB) *AnalyticsRepository {
	return &AnalyticsRepository{db: db}
}

// Upsert folds each flushed rollup into its stored row for the same
// (workspace, app, route, minute) key: a new key inserts, an existing key merges
// (counters add, histograms add element-wise, top-K maps combine, HLL sketches
// merge). Done under a row lock in one transaction so concurrent consumers — or a
// re-flush after a crash — never lose or double-count a bucket.
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

// Range returns every rollup for a workspace whose bucket falls in [since, until),
// oldest first. appID filters to a single application when non-nil (0 means "all
// apps in the workspace", so a nil filter is the whole workspace).
func (r *AnalyticsRepository) Range(workspaceID uint, appID *uint, since, until time.Time) ([]models.AnalyticsRollup, error) {
	q := r.db.Where("workspace_id = ? AND bucket >= ? AND bucket < ?", workspaceID, since, until)
	if appID != nil {
		q = q.Where("application_id = ?", *appID)
	}
	var rows []models.AnalyticsRollup
	err := q.Order("bucket ASC").Find(&rows).Error
	return rows, err
}

// summaryColumns are the columns BuildSummary reads. The seven top-K maps and
// the upstream histogram are JSON blobs that gorm deserializes on read whether
// or not the caller looks at them — and they dwarf the counters in a row. Naming
// the columns keeps the dashboard's query to the counters, the latency histogram
// and the visitor sketch.
var summaryColumns = []string{
	"id", "bucket", "requests", "bytes_in", "bytes_out",
	"status2xx", "status3xx", "status4xx", "status5xx",
	"duration_hist", "duration_sum", "visitors_hll",
}

var summaryGeoColumns = append(append([]string{}, summaryColumns...), "top_countries")

// RangeSummary is Range with only the columns the summary needs. Same rows, same
// order — a fraction of the bytes and none of the top-K decoding.
//
// withGeo additionally reads top_countries, for the one window whose countries
// are rendered. The period-over-period comparison window passes false: only its
// totals are read, so decoding its countries would be pure waste.
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

// AppIDs lists the distinct application ids that have analytics data in the
// window, so the UI can populate its app filter with only apps that have traffic.
func (r *AnalyticsRepository) AppIDs(workspaceID uint, since, until time.Time) ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.AnalyticsRollup{}).
		Where("workspace_id = ? AND bucket >= ? AND bucket < ? AND application_id > 0", workspaceID, since, until).
		Distinct().Pluck("application_id", &ids).Error
	return ids, err
}

// Prune deletes rollups older than `before`, returning the number removed — the
// retention job's unit of work.
func (r *AnalyticsRepository) Prune(before time.Time) (int64, error) {
	res := r.db.Where("bucket < ?", before).Delete(&models.AnalyticsRollup{})
	return res.RowsAffected, res.Error
}
