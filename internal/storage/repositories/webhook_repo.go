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

type WebhookRepository struct {
	db *gorm.DB
}

func NewWebhookRepository(db *gorm.DB) *WebhookRepository { return &WebhookRepository{db: db} }

func (r *WebhookRepository) Create(w *models.Webhook) error { return r.db.Create(w).Error }
func (r *WebhookRepository) Update(w *models.Webhook) error { return r.db.Save(w).Error }

// Delete removes a webhook and its delivery history in a single transaction.
func (r *WebhookRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("webhook_id = ?", id).Delete(&models.WebhookDelivery{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Webhook{}, id).Error
	})
}

// FindByID ignores workspace; only the delivery worker uses it, which already trusts the enqueued id.
func (r *WebhookRepository) FindByID(id uint) (*models.Webhook, error) {
	var w models.Webhook
	if err := r.db.First(&w, id).Error; err != nil {
		return nil, err
	}
	return &w, nil
}

func (r *WebhookRepository) FindInWorkspace(workspaceID, id uint) (*models.Webhook, error) {
	var w models.Webhook
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&w).Error; err != nil {
		return nil, err
	}
	return &w, nil
}

func (r *WebhookRepository) ListByWorkspace(workspaceID uint) ([]models.Webhook, error) {
	var ws []models.Webhook
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&ws).Error
	return ws, err
}

// The subscription list is a JSON column, so membership is filtered in Go.
func (r *WebhookRepository) ListEnabledForEvent(workspaceID uint, event string) ([]models.Webhook, error) {
	var ws []models.Webhook
	if err := r.db.Where("workspace_id = ? AND enabled = ?", workspaceID, true).Find(&ws).Error; err != nil {
		return nil, err
	}
	out := ws[:0]
	for _, w := range ws {
		if containsString(w.Events, event) {
			out = append(out, w)
		}
	}
	return out, nil
}

type WebhookDeliveryRepository struct {
	db *gorm.DB
}

func NewWebhookDeliveryRepository(db *gorm.DB) *WebhookDeliveryRepository {
	return &WebhookDeliveryRepository{db: db}
}

func (r *WebhookDeliveryRepository) Create(d *models.WebhookDelivery) error {
	return r.db.Create(d).Error
}

// FindInWorkspace returns a delivery by id, scoped to the workspace.
func (r *WebhookDeliveryRepository) FindInWorkspace(workspaceID, id uint) (*models.WebhookDelivery, error) {
	var d models.WebhookDelivery
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// Scoped to the workspace so callers cannot read another tenant's history.
func (r *WebhookDeliveryRepository) ListForWebhook(workspaceID, webhookID uint, limit int) ([]models.WebhookDelivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var ds []models.WebhookDelivery
	err := r.db.Where("workspace_id = ? AND webhook_id = ?", workspaceID, webhookID).
		Order("id DESC").Limit(limit).Find(&ds).Error
	return ds, err
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
