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

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/netguard"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/webhook"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// Returning an error lets asynq retry with exponential backoff.
type WebhookDeliverHandler struct {
	webhooks   *repositories.WebhookRepository
	deliveries *repositories.WebhookDeliveryRepository
	events     *repositories.AppEventRepository
	apps       *repositories.ApplicationRepository
	databases  *repositories.DatabaseRepository
	client     *http.Client
}

func NewWebhookDeliverHandler(webhooks *repositories.WebhookRepository, deliveries *repositories.WebhookDeliveryRepository, events *repositories.AppEventRepository, apps *repositories.ApplicationRepository, databases *repositories.DatabaseRepository) *WebhookDeliverHandler {
	return &WebhookDeliverHandler{
		webhooks:   webhooks,
		deliveries: deliveries,
		events:     events,
		apps:       apps,
		databases:  databases,
		client:     netguard.Client(10 * time.Second),
	}
}

func (h *WebhookDeliverHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p WebhookDeliverPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad webhook delivery payload: %w", err)
	}
	w, err := h.webhooks.FindByID(p.WebhookID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // webhook deleted; drop
		}
		return err
	}
	if !w.Enabled {
		return nil
	}
	e, err := h.events.FindByID(p.EventID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}

	enrichEvent(e, h.apps, h.databases)

	secret, err := crypto.Decrypt(w.Secret)
	if err != nil {
		return fmt.Errorf("decrypt webhook secret: %w", err)
	}
	body, err := webhook.BuildPayload(e)
	if err != nil {
		return err
	}

	retries, _ := asynq.GetRetryCount(ctx)
	attempt := retries + 1
	status, derr := webhook.Deliver(ctx, h.client, w.URL, secret, w.Headers, body)

	delivery := &models.WebhookDelivery{
		WebhookID:      w.ID,
		WorkspaceID:    w.WorkspaceID,
		Event:          string(e.Type),
		EventID:        e.ID,
		HTTPStatusCode: status,
		Attempt:        attempt,
	}
	if derr == nil && status < 400 {
		delivery.Status = models.WebhookDeliverySuccess
		if cerr := h.deliveries.Create(delivery); cerr != nil {
			logger.Error("failed to record webhook delivery", "webhook", w.ID, "error", cerr)
		}
		return nil
	}

	delivery.Status = models.WebhookDeliveryFailed
	if derr != nil {
		delivery.ErrorMessage = derr.Error()
	} else {
		delivery.ErrorMessage = fmt.Sprintf("HTTP %d", status)
	}
	if cerr := h.deliveries.Create(delivery); cerr != nil {
		logger.Error("failed to record webhook delivery", "webhook", w.ID, "error", cerr)
	}
	if derr != nil {
		return derr
	}
	return fmt.Errorf("webhook %d returned status %d", w.ID, status)
}
