// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NotificationInboxRepository persists per-user inbox items (models.Notification).
// Distinct from NotificationChannelRepository (outbound transports).
type NotificationInboxRepository struct {
	db *gorm.DB
}

func NewNotificationInboxRepository(db *gorm.DB) *NotificationInboxRepository {
	return &NotificationInboxRepository{db: db}
}

// Upsert creates the notification, or updates the existing one for the same
// (user, alert) in place — so an alert's count bump or auto-resolve refreshes the
// bell item instead of adding a row. resurface marks it unread again (a state
// change like firing→resolved is worth re-showing; a plain count bump is not).
func (r *NotificationInboxRepository) Upsert(n *models.Notification, resurface bool) error {
	if n.AlertID == nil {
		return r.db.Create(n).Error // standalone info: always a new row
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		var existing models.Notification
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND alert_id = ?", n.UserID, *n.AlertID).
			First(&existing).Error
		switch {
		case err == gorm.ErrRecordNotFound:
			return tx.Create(n).Error
		case err != nil:
			return err
		default:
			existing.Title = n.Title
			existing.Body = n.Body
			existing.Severity = n.Severity
			existing.Category = n.Category
			existing.SubjectLink = n.SubjectLink
			existing.Kind = n.Kind
			if resurface {
				existing.ReadAt = nil
			}
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			*n = existing
			return nil
		}
	})
}

// ListByUser returns a user's notifications across the workspaces they belong to,
// newest first. Filters: workspaceID (0=all), unreadOnly, before (keyset paging).
func (r *NotificationInboxRepository) ListByUser(userID uint, workspaceID uint, unreadOnly bool, before uint, limit int) ([]models.Notification, error) {
	q := r.db.Where("user_id = ?", userID)
	if workspaceID > 0 {
		q = q.Where("workspace_id = ?", workspaceID)
	}
	if unreadOnly {
		q = q.Where("read_at IS NULL")
	}
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	var out []models.Notification
	err := q.Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

// UnreadCount returns the user's unread notification count (bell badge).
func (r *NotificationInboxRepository) UnreadCount(userID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).Count(&n).Error
	return n, err
}

// MarkRead marks the given notification ids read for the user (ownership-scoped).
func (r *NotificationInboxRepository) MarkRead(userID uint, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Model(&models.Notification{}).
		Where("user_id = ? AND id IN ? AND read_at IS NULL", userID, ids).
		Update("read_at", time.Now().UTC()).Error
}

// MarkAllRead marks every unread notification read for the user, optionally
// scoped to one workspace.
func (r *NotificationInboxRepository) MarkAllRead(userID, workspaceID uint) error {
	q := r.db.Model(&models.Notification{}).Where("user_id = ? AND read_at IS NULL", userID)
	if workspaceID > 0 {
		q = q.Where("workspace_id = ?", workspaceID)
	}
	return q.Update("read_at", time.Now().UTC()).Error
}

// ApplyAlertUpdate updates every notification tied to an alert in place (e.g. an
// auto-resolve rewriting the title to "recovered") and returns the affected user
// ids so the engine can push the change over SSE. resurface marks them unread
// again — a firing→resolved transition is worth re-showing. One UPDATE, one
// SELECT; no per-user loop.
func (r *NotificationInboxRepository) ApplyAlertUpdate(alertID uint, tmpl models.Notification, resurface bool) ([]uint, error) {
	updates := map[string]any{
		"title":        tmpl.Title,
		"body":         tmpl.Body,
		"severity":     tmpl.Severity,
		"kind":         tmpl.Kind,
		"subject_link": tmpl.SubjectLink,
		"updated_at":   time.Now().UTC(),
	}
	if resurface {
		updates["read_at"] = nil
	}
	if err := r.db.Model(&models.Notification{}).Where("alert_id = ?", alertID).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	var userIDs []uint
	err := r.db.Model(&models.Notification{}).Where("alert_id = ?", alertID).
		Distinct().Pluck("user_id", &userIDs).Error
	return userIDs, err
}

// Prune deletes notifications older than `before` (retention).
func (r *NotificationInboxRepository) Prune(before time.Time) (int64, error) {
	res := r.db.Where("created_at < ?", before).Delete(&models.Notification{})
	return res.RowsAffected, res.Error
}
