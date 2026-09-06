// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/services/notify"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// containerEventDebounce coalesces repeated container.* notifications for the
// same app (e.g. a crash-looping container) within this window. Deploy results
// are never suppressed.
const containerEventDebounce = 60 * time.Second

// FanoutHandler resolves a recorded event into per-endpoint delivery tasks for
// the event's workspace.
type FanoutHandler struct {
	webhooks *repositories.WebhookRepository
	channels *repositories.NotificationChannelRepository
	events   *repositories.AppEventRepository
	producer *Producer
	rdb      *redis.Client
}

func NewFanoutHandler(webhooks *repositories.WebhookRepository, channels *repositories.NotificationChannelRepository, events *repositories.AppEventRepository, producer *Producer, rdb *redis.Client) *FanoutHandler {
	return &FanoutHandler{webhooks: webhooks, channels: channels, events: events, producer: producer, rdb: rdb}
}

func (h *FanoutHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p NotifyFanoutPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad notify fan-out payload: %w", err)
	}
	e, err := h.events.FindByID(p.AppEventID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // event trimmed/removed; nothing to do
		}
		return err
	}
	event := string(e.Type)

	// Suppress repeated container.* notifications for a flapping app. The event
	// is still recorded on the timeline; only outbound delivery is throttled.
	if strings.HasPrefix(event, "container.") && h.suppressed(ctx, e.WorkspaceID, e.ApplicationID, event) {
		logger.Debug("notification suppressed (debounced)", "app", e.ApplicationID, "type", event)
		return nil
	}

	hooks, err := h.webhooks.ListEnabledForEvent(e.WorkspaceID, event)
	if err != nil {
		return err
	}
	for _, w := range hooks {
		if err := h.producer.EnqueueWebhookDeliver(w.ID, e.ID); err != nil {
			logger.Error("failed to enqueue webhook delivery", "webhook", w.ID, "event", e.ID, "error", err)
		}
	}

	channels, err := h.channels.ListEnabledForEvent(e.WorkspaceID, event)
	if err != nil {
		return err
	}
	for _, ch := range channels {
		if err := h.producer.EnqueueNotifyChannel(ch.ID, e.ID); err != nil {
			logger.Error("failed to enqueue channel notification", "channel", ch.ID, "event", e.ID, "error", err)
		}
	}
	return nil
}

// suppressed reports whether an identical (workspace, app, event) notification
// fired within the debounce window. The first occurrence claims the key (and is
// allowed); subsequent ones within the window are suppressed. Fails open.
func (h *FanoutHandler) suppressed(ctx context.Context, workspaceID, appID uint, event string) bool {
	if h.rdb == nil {
		return false
	}
	key := fmt.Sprintf("notif-debounce:%d:%d:%s", workspaceID, appID, event)
	ok, err := h.rdb.SetNX(ctx, key, 1, containerEventDebounce).Result()
	if err != nil {
		return false // fail open
	}
	return !ok
}

// ChannelSendHandler delivers one event to one notification channel.
type ChannelSendHandler struct {
	channels  *repositories.NotificationChannelRepository
	events    *repositories.AppEventRepository
	apps      *repositories.ApplicationRepository
	databases *repositories.DatabaseRepository
	registry  *notify.Registry
}

func NewChannelSendHandler(channels *repositories.NotificationChannelRepository, events *repositories.AppEventRepository, apps *repositories.ApplicationRepository, databases *repositories.DatabaseRepository, registry *notify.Registry) *ChannelSendHandler {
	return &ChannelSendHandler{channels: channels, events: events, apps: apps, databases: databases, registry: registry}
}

func (h *ChannelSendHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p NotifyChannelPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad channel-send payload: %w", err)
	}
	ch, err := h.channels.FindByID(p.ChannelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // channel deleted; drop
		}
		return err
	}
	if !ch.Enabled {
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
	if err := h.registry.Send(ctx, ch, e); err != nil {
		logger.Error("notification channel delivery failed", "channel", ch.ID, "event", e.ID, "error", err)
		return err // let asynq retry with backoff
	}
	return nil
}
