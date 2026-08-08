// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type AuditLogRepository struct {
	db *gorm.DB
}

func NewAuditLogRepository(db *gorm.DB) *AuditLogRepository { return &AuditLogRepository{db: db} }

// applyTimeRange narrows a query to a created_at window. Zero bounds are skipped; the upper
// bound is exclusive (the caller advances a date-only `to`).
func applyTimeRange(q *gorm.DB, from, to time.Time) *gorm.DB {
	if !from.IsZero() {
		q = q.Where("created_at >= ?", from)
	}
	if !to.IsZero() {
		q = q.Where("created_at < ?", to)
	}
	return q
}

func (r *AuditLogRepository) Create(entry *models.AuditLog) error {
	return r.db.Create(entry).Error
}

// ListByWorkspace returns recent audit entries for a workspace, newest first.
func (r *AuditLogRepository) ListByWorkspace(workspaceID uint, order string, from, to time.Time, limit, offset int) ([]models.AuditLog, int64, error) {
	var (
		entries []models.AuditLog
		total   int64
	)
	q := r.db.Model(&models.AuditLog{}).Where("workspace_id = ?", workspaceID)
	q = applyTimeRange(q, from, to)
	q.Count(&total)
	if err := q.Order("created_at " + orderDir(order)).Limit(limit).Offset(offset).Find(&entries).Error; err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// orderDir sanitizes a caller-supplied sort direction, defaulting to DESC. Only the two literal
// keywords are ever returned, so the result is safe to concatenate into an ORDER BY clause.
func orderDir(order string) string {
	if strings.EqualFold(strings.TrimSpace(order), "asc") {
		return "ASC"
	}
	return "DESC"
}

// FindAll returns recent audit entries across every workspace (platform admin
// view), newest first. The optional search term matches action/target, and the
// optional action filter narrows to a single action.
func (r *AuditLogRepository) FindAll(search, action, order string, from, to time.Time, limit, offset int) ([]models.AuditLog, int64, error) {
	var (
		entries []models.AuditLog
		total   int64
	)
	q := r.db.Model(&models.AuditLog{})
	if a := strings.TrimSpace(action); a != "" {
		q = q.Where("action = ?", a)
	}
	if s := strings.TrimSpace(search); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(action) LIKE ? OR LOWER(target_type) LIKE ? OR LOWER(target_id) LIKE ?", like, like, like)
	}
	q = applyTimeRange(q, from, to)
	q.Count(&total)
	if err := q.Order("created_at " + orderDir(order)).Limit(limit).Offset(offset).Find(&entries).Error; err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// DeleteOlderThan prunes audit entries created before cutoff and returns the
// number removed. Used by the retention cron.
func (r *AuditLogRepository) DeleteOlderThan(cutoff time.Time) (int64, error) {
	res := r.db.Where("created_at < ?", cutoff).Delete(&models.AuditLog{})
	return res.RowsAffected, res.Error
}

// Since returns audit entries with id greater than afterID, oldest first — the
// cursor read used by the SIEM streamer for at-least-once delivery.
func (r *AuditLogRepository) Since(afterID uint, limit int) ([]models.AuditLog, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	var entries []models.AuditLog
	err := r.db.Where("id > ?", afterID).Order("id ASC").Limit(limit).Find(&entries).Error
	return entries, err
}

// FindByID returns a single audit entry.
func (r *AuditLogRepository) FindByID(id uint) (*models.AuditLog, error) {
	var entry models.AuditLog
	if err := r.db.First(&entry, id).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// ListByActor returns a user's recent audit entries (actions they performed),
// newest first.
func (r *AuditLogRepository) ListByActor(actorID uint, limit int) ([]models.AuditLog, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var entries []models.AuditLog
	err := r.db.Where("actor_id = ?", actorID).Order("created_at DESC").Limit(limit).Find(&entries).Error
	return entries, err
}
