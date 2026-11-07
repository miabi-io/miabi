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
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type NotificationChannelRepository struct {
	db *gorm.DB
}

func NewNotificationChannelRepository(db *gorm.DB) *NotificationChannelRepository {
	return &NotificationChannelRepository{db: db}
}

func (r *NotificationChannelRepository) Create(ch *models.NotificationChannel) error {
	return r.db.Create(ch).Error
}
func (r *NotificationChannelRepository) Update(ch *models.NotificationChannel) error {
	return r.db.Save(ch).Error
}
func (r *NotificationChannelRepository) Delete(id uint) error {
	return r.db.Delete(&models.NotificationChannel{}, id).Error
}

// Unscoped: the channel-send worker already trusts the enqueued id.
func (r *NotificationChannelRepository) FindByID(id uint) (*models.NotificationChannel, error) {
	var ch models.NotificationChannel
	if err := r.db.First(&ch, id).Error; err != nil {
		return nil, err
	}
	return &ch, nil
}

func (r *NotificationChannelRepository) FindInWorkspace(workspaceID, id uint) (*models.NotificationChannel, error) {
	var ch models.NotificationChannel
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&ch).Error; err != nil {
		return nil, err
	}
	return &ch, nil
}

func (r *NotificationChannelRepository) ListByWorkspace(workspaceID uint) ([]models.NotificationChannel, error) {
	var chs []models.NotificationChannel
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&chs).Error
	return chs, err
}

// Event membership is filtered in Go; see WebhookRepository.
func (r *NotificationChannelRepository) ListEnabledForEvent(workspaceID uint, event string) ([]models.NotificationChannel, error) {
	var chs []models.NotificationChannel
	if err := r.db.Where("workspace_id = ? AND enabled = ?", workspaceID, true).Find(&chs).Error; err != nil {
		return nil, err
	}
	out := chs[:0]
	for _, ch := range chs {
		if containsString(ch.Events, event) {
			out = append(out, ch)
		}
	}
	return out, nil
}
