// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type AppEventRepository struct {
	db *gorm.DB
}

func NewAppEventRepository(db *gorm.DB) *AppEventRepository { return &AppEventRepository{db: db} }

func (r *AppEventRepository) Create(e *models.AppEvent) error { return r.db.Create(e).Error }

func (r *AppEventRepository) FindByID(id uint) (*models.AppEvent, error) {
	var e models.AppEvent
	if err := r.db.First(&e, id).Error; err != nil {
		return nil, err
	}
	return &e, nil
}

// ListByApp returns events newest-first. When before > 0, only events with a
// smaller ID are returned (cursor pagination).
func (r *AppEventRepository) ListByApp(appID uint, limit int, before uint) ([]models.AppEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := r.db.Where("application_id = ?", appID)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	var events []models.AppEvent
	err := q.Order("id DESC").Limit(limit).Find(&events).Error
	return events, err
}

// ListByWorkspace returns the workspace's application events newest-first,
// across all of its applications. Used for the dashboard activity feed.
func (r *AppEventRepository) ListByWorkspace(workspaceID uint, limit int) ([]models.AppEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var events []models.AppEvent
	err := r.db.Where("workspace_id = ?", workspaceID).
		Order("id DESC").Limit(limit).Find(&events).Error
	return events, err
}

// ListByWorkspacePaged returns the workspace's application events with offset pagination and
// the total count. order is "asc" or "desc" (default); severity, when non-empty, filters to a
// single severity.
func (r *AppEventRepository) ListByWorkspacePaged(workspaceID uint, order, severity string, limit, offset int) ([]models.AppEvent, int64, error) {
	var (
		events []models.AppEvent
		total  int64
	)
	q := r.db.Model(&models.AppEvent{}).Where("workspace_id = ?", workspaceID)
	if s := strings.TrimSpace(severity); s != "" {
		q = q.Where("severity = ?", s)
	}
	q.Count(&total)
	if err := q.Order("id " + orderDir(order)).Limit(limit).Offset(offset).Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// ListByApps returns events newest-first across the given application ids.
// Used for a stack's combined activity feed.
func (r *AppEventRepository) ListByApps(appIDs []uint, limit int) ([]models.AppEvent, error) {
	if len(appIDs) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	var events []models.AppEvent
	err := r.db.Where("application_id IN ?", appIDs).
		Order("id DESC").Limit(limit).Find(&events).Error
	return events, err
}

// TrimByApp deletes all but the most recent keep events for an application.
func (r *AppEventRepository) TrimByApp(appID uint, keep int) error {
	var cutoff uint
	err := r.db.Model(&models.AppEvent{}).
		Where("application_id = ?", appID).
		Order("id DESC").
		Offset(keep).Limit(1).
		Select("id").Scan(&cutoff).Error
	if err != nil || cutoff == 0 {
		return err
	}
	return r.db.Where("application_id = ? AND id <= ?", appID, cutoff).
		Delete(&models.AppEvent{}).Error
}
