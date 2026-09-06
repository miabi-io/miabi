// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// AnnouncementRepository persists platform announcements and resolves their
// audiences. Deliveries live in NotificationInboxRepository.
type AnnouncementRepository struct {
	db *gorm.DB
}

func NewAnnouncementRepository(db *gorm.DB) *AnnouncementRepository {
	return &AnnouncementRepository{db: db}
}

func (r *AnnouncementRepository) Create(a *models.Announcement) error {
	return r.db.Create(a).Error
}

func (r *AnnouncementRepository) Save(a *models.Announcement) error {
	return r.db.Save(a).Error
}

func (r *AnnouncementRepository) FindByID(id uint) (*models.Announcement, error) {
	var a models.Announcement
	if err := r.db.First(&a, id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *AnnouncementRepository) Delete(id uint) error {
	return r.db.Delete(&models.Announcement{}, id).Error
}

// List returns a page of announcements, newest first, with the total for pagination.
func (r *AnnouncementRepository) List(limit, offset int) ([]models.Announcement, int64, error) {
	var (
		rows  []models.Announcement
		total int64
	)
	if err := r.db.Model(&models.Announcement{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := r.db.Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

// DueForPublish returns scheduled announcements whose publish time has arrived.
func (r *AnnouncementRepository) DueForPublish(now time.Time) ([]models.Announcement, error) {
	var rows []models.Announcement
	err := r.db.Where("published_at IS NULL AND publish_at IS NOT NULL AND publish_at <= ?", now).
		Order("id").Find(&rows).Error
	return rows, err
}

// Live returns published, unexpired announcements — the set a user who has not
// received them yet should still be caught up on.
func (r *AnnouncementRepository) Live(now time.Time) ([]models.Announcement, error) {
	var rows []models.Announcement
	err := r.db.Where("published_at IS NOT NULL AND published_at <= ?", now).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Order("id").Find(&rows).Error
	return rows, err
}

// UndeliveredLive returns the live announcements the user has no delivery for.
// It is the backfill query: a user created (or invited into a targeted workspace)
// after a broadcast still gets the notice, which a one-shot fan-out cannot do.
func (r *AnnouncementRepository) UndeliveredLive(userID uint, now time.Time) ([]models.Announcement, error) {
	var rows []models.Announcement
	err := r.db.Where("published_at IS NOT NULL AND published_at <= ?", now).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Where("NOT EXISTS (SELECT 1 FROM notifications n WHERE n.announcement_id = announcements.id AND n.user_id = ?)", userID).
		Order("id").Find(&rows).Error
	return rows, err
}

// RecipientIDs resolves an announcement's audience to user ids. Suspended accounts
// are excluded everywhere: a notice they cannot sign in to read is not a delivery.
func (r *AnnouncementRepository) RecipientIDs(a *models.Announcement) ([]uint, error) {
	q := r.db.Model(&models.User{}).Where("active = ?", true)
	switch a.Audience {
	case models.AudienceAdmins:
		q = q.Where("role = ?", models.SystemRoleAdmin)
	case models.AudienceOwners:
		q = q.Where("id IN (?)", r.db.Model(&models.WorkspaceMember{}).
			Select("user_id").
			Where("role IN ?", []models.WorkspaceRole{models.WorkspaceRoleOwner, models.WorkspaceRoleAdmin}))
	case models.AudienceWorkspaces:
		if len(a.WorkspaceIDs) == 0 {
			return nil, nil
		}
		q = q.Where("id IN (?)", r.db.Model(&models.WorkspaceMember{}).
			Select("user_id").Where("workspace_id IN ?", a.WorkspaceIDs))
	}
	var ids []uint
	err := q.Pluck("id", &ids).Error
	return ids, err
}

// Matches reports whether one user falls inside an announcement's audience. The
// backfill asks per user, where re-resolving the whole audience would be wasteful.
func (r *AnnouncementRepository) Matches(a *models.Announcement, userID uint) (bool, error) {
	q := r.db.Model(&models.User{}).Where("id = ? AND active = ?", userID, true)
	switch a.Audience {
	case models.AudienceAdmins:
		q = q.Where("role = ?", models.SystemRoleAdmin)
	case models.AudienceOwners:
		q = q.Where("id IN (?)", r.db.Model(&models.WorkspaceMember{}).
			Select("user_id").
			Where("role IN ?", []models.WorkspaceRole{models.WorkspaceRoleOwner, models.WorkspaceRoleAdmin}))
	case models.AudienceWorkspaces:
		if len(a.WorkspaceIDs) == 0 {
			return false, nil
		}
		q = q.Where("id IN (?)", r.db.Model(&models.WorkspaceMember{}).
			Select("user_id").Where("workspace_id IN ?", a.WorkspaceIDs))
	}
	var n int64
	err := q.Count(&n).Error
	return n > 0, err
}

// CountRecipients previews an audience's size before the operator broadcasts.
func (r *AnnouncementRepository) CountRecipients(a *models.Announcement) (int, error) {
	ids, err := r.RecipientIDs(a)
	return len(ids), err
}
