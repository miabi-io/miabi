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

func (r *AppEventRepository) ListByDatabase(databaseID uint, limit int, before uint) ([]models.AppEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := r.db.Where("database_id = ?", databaseID)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	var events []models.AppEvent
	err := q.Order("id DESC").Limit(limit).Find(&events).Error
	return events, err
}

func (r *AppEventRepository) ListByWorkspace(workspaceID uint, limit int) ([]models.AppEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var events []models.AppEvent
	err := r.db.Where("workspace_id = ?", workspaceID).
		Order("id DESC").Limit(limit).Find(&events).Error
	return events, err
}

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

// TrimByDatabase deletes all but the most recent keep events for a database instance.
func (r *AppEventRepository) TrimByDatabase(databaseID uint, keep int) error {
	var cutoff uint
	err := r.db.Model(&models.AppEvent{}).
		Where("database_id = ?", databaseID).
		Order("id DESC").
		Offset(keep).Limit(1).
		Select("id").Scan(&cutoff).Error
	if err != nil || cutoff == 0 {
		return err
	}
	return r.db.Where("database_id = ? AND id <= ?", databaseID, cutoff).
		Delete(&models.AppEvent{}).Error
}

func (r *AppEventRepository) DeleteByDatabase(databaseID uint) error {
	return r.db.Where("database_id = ?", databaseID).Delete(&models.AppEvent{}).Error
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
