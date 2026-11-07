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
	"gorm.io/gorm/clause"
)

// Distinct from NotificationChannelRepository (outbound transports).
type NotificationInboxRepository struct {
	db *gorm.DB
}

func NewNotificationInboxRepository(db *gorm.DB) *NotificationInboxRepository {
	return &NotificationInboxRepository{db: db}
}

// Updates in place per (user, alert); resurface marks unread again, for state changes only.
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

// Platform-scoped items (workspace 0, e.g. announcements) follow the user into every workspace.
func (r *NotificationInboxRepository) ListByUser(userID uint, workspaceID uint, unreadOnly bool, before uint, limit int) ([]models.Notification, error) {
	q := r.db.Where("user_id = ?", userID)
	if workspaceID > 0 {
		q = q.Where("workspace_id IN ?", []uint{workspaceID, 0})
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

// An expired announcement stops counting so a past maintenance window doesn't keep the badge lit.
func (r *NotificationInboxRepository) UnreadCount(userID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Where("expires_at IS NULL OR expires_at > ?", time.Now().UTC()).
		Count(&n).Error
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

// Scope matches ListByUser's, so "mark all read" clears exactly what the list showed.
func (r *NotificationInboxRepository) MarkAllRead(userID, workspaceID uint) error {
	q := r.db.Model(&models.Notification{}).Where("user_id = ? AND read_at IS NULL", userID)
	if workspaceID > 0 {
		q = q.Where("workspace_id IN ?", []uint{workspaceID, 0})
	}
	return q.Update("read_at", time.Now().UTC()).Error
}

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

// Live announcement deliveries are exempt: the backfill would re-create them as unread.
func (r *NotificationInboxRepository) Prune(before time.Time) (int64, error) {
	now := time.Now().UTC()
	res := r.db.Where("created_at < ?", before).
		Where(`announcement_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM announcements a WHERE a.id = notifications.announcement_id
			AND a.published_at IS NOT NULL AND a.published_at <= ?
			AND (a.expires_at IS NULL OR a.expires_at > ?))`, now, now).
		Delete(&models.Notification{})
	return res.RowsAffected, res.Error
}

// The conflict clause makes it idempotent so a fan-out racing the backfill can't double-deliver.
func (r *NotificationInboxRepository) CreateBatch(rows []models.Notification) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	res := r.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 200)
	return res.RowsAffected, res.Error
}

// An edited notice corrects itself in each inbox instead of appending a contradictory one.
func (r *NotificationInboxRepository) ApplyAnnouncementUpdate(announcementID uint, tmpl models.Notification) ([]uint, error) {
	updates := map[string]any{
		"title":        tmpl.Title,
		"body":         tmpl.Body,
		"severity":     tmpl.Severity,
		"subject_link": tmpl.SubjectLink,
		"action_text":  tmpl.ActionText,
		"pinned":       tmpl.Pinned,
		"dismissal":    tmpl.Dismissal,
		"expires_at":   tmpl.ExpiresAt,
		"updated_at":   time.Now().UTC(),
	}
	if err := r.db.Model(&models.Notification{}).
		Where("announcement_id = ?", announcementID).Updates(updates).Error; err != nil {
		return nil, err
	}
	var userIDs []uint
	err := r.db.Model(&models.Notification{}).Where("announcement_id = ?", announcementID).
		Distinct().Pluck("user_id", &userIDs).Error
	return userIDs, err
}

func (r *NotificationInboxRepository) DeleteForAnnouncement(announcementID uint) ([]uint, error) {
	var userIDs []uint
	if err := r.db.Model(&models.Notification{}).Where("announcement_id = ?", announcementID).
		Distinct().Pluck("user_id", &userIDs).Error; err != nil {
		return nil, err
	}
	err := r.db.Where("announcement_id = ?", announcementID).Delete(&models.Notification{}).Error
	return userIDs, err
}

// CountForAnnouncement returns how many deliveries an announcement has produced.
func (r *NotificationInboxRepository) CountForAnnouncement(announcementID uint) (int64, error) {
	var n int64
	err := r.db.Model(&models.Notification{}).Where("announcement_id = ?", announcementID).
		Count(&n).Error
	return n, err
}

// Undismissable notices sort first so the limit never quietly takes one off the reader's screen.
func (r *NotificationInboxRepository) Banners(userID uint, now time.Time) ([]models.Notification, error) {
	var out []models.Notification
	err := r.db.Where("user_id = ? AND pinned = ? AND dismissed_at IS NULL", userID, true).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Order("CASE WHEN dismissal = 'never' THEN 0 ELSE 1 END, " +
			"CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, id DESC").
		Limit(5).Find(&out).Error
	return out, err
}

// Undismissable rows are skipped here, not only in the UI, so a hand-made request can't hide them.
func (r *NotificationInboxRepository) Dismiss(userID uint, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return r.db.Model(&models.Notification{}).
		Where("user_id = ? AND id IN ? AND dismissed_at IS NULL", userID, ids).
		Where("dismissal <> ?", models.DismissNever).
		Updates(map[string]any{"dismissed_at": now, "read_at": gorm.Expr("COALESCE(read_at, ?)", now)}).Error
}
