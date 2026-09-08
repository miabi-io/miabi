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

// Upsert creates the notification or updates the existing one for the same (user, alert) in
// place, so a count bump or auto-resolve refreshes the bell item instead of adding a row.
// resurface marks it unread again — worth it for a state change, not for a plain count bump.
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
// Filtering by a workspace keeps platform-scoped items (workspace 0, e.g.
// announcements): they follow the user into every workspace they open.
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

// UnreadCount returns the user's unread notification count (bell badge). An
// expired announcement stops counting: a maintenance window that has passed
// should not keep the badge lit until someone clicks it away.
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

// MarkAllRead marks every unread notification read for the user, optionally
// scoped to one workspace. The scope matches ListByUser's, platform-scoped items
// included, so "mark all read" clears exactly what the list showed.
func (r *NotificationInboxRepository) MarkAllRead(userID, workspaceID uint) error {
	q := r.db.Model(&models.Notification{}).Where("user_id = ? AND read_at IS NULL", userID)
	if workspaceID > 0 {
		q = q.Where("workspace_id IN ?", []uint{workspaceID, 0})
	}
	return q.Update("read_at", time.Now().UTC()).Error
}

// ApplyAlertUpdate updates every notification tied to an alert in place (e.g. an auto-resolve
// rewriting the title) and returns the affected user ids so the engine can push over SSE.
// resurface marks them unread again. One UPDATE, one SELECT; no per-user loop.
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

// Prune deletes notifications older than `before` (retention). Deliveries of an
// announcement that is still live are exempt: deleting one only makes the backfill
// re-create it on the reader's next inbox read, resurrecting as unread a notice
// they had already read or dismissed.
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

// CreateBatch inserts many deliveries at once — the announcement fan-out. The
// conflict clause makes it idempotent against the (user, announcement) unique
// index, so a fan-out racing the per-user backfill cannot double-deliver.
func (r *NotificationInboxRepository) CreateBatch(rows []models.Notification) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	res := r.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 200)
	return res.RowsAffected, res.Error
}

// ApplyAnnouncementUpdate rewrites every delivery of an announcement in place and
// returns the affected user ids so the caller can push over SSE. An edited notice
// corrects itself in each inbox instead of appending a second, contradictory one.
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

// DeleteForAnnouncement removes the deliveries a retracted announcement made and
// returns the users whose inbox changed. An operator who broadcast the wrong thing
// takes it back rather than appending a correction to everybody's inbox.
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

// Banners returns the pinned, live, undismissed items for a user — what the
// app-wide banner renders, newest and most severe first. Undismissable notices
// sort ahead of everything else: the reader cannot clear them, so the limit below
// must not be what quietly takes one off their screen.
func (r *NotificationInboxRepository) Banners(userID uint, now time.Time) ([]models.Notification, error) {
	var out []models.Notification
	err := r.db.Where("user_id = ? AND pinned = ? AND dismissed_at IS NULL", userID, true).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Order("CASE WHEN dismissal = 'never' THEN 0 ELSE 1 END, " +
			"CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, id DESC").
		Limit(5).Find(&out).Error
	return out, err
}

// Dismiss hides items from the banner. It also marks them read: someone who
// dismissed a notice has seen it, and leaving the bell badge lit would be noise.
// Rows the operator marked undismissable are skipped here and not only in the UI,
// so hiding one takes retracting or expiring it rather than a hand-made request.
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
