// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// FindByID returns a webhook by primary key, regardless of workspace. Used by
// the delivery worker, which already trusts the enqueued id.
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

// ListEnabledForEvent returns enabled webhooks in the workspace that subscribe
// to the given event. The subscription list is a JSON column, so membership is
// filtered in Go (workspaces hold few webhooks).
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

// ListForWebhook returns a webhook's deliveries newest-first, scoped to the
// workspace so callers cannot read another tenant's history.
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
