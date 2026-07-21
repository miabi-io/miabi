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
