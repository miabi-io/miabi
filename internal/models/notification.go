// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// NotifiableEvents is the curated set of application lifecycle events that
// webhooks and notification channels may subscribe to. Configuration changes
// and other timeline noise are intentionally excluded.
var NotifiableEvents = []AppEventType{
	EventDeployStarted,
	EventDeploySucceeded,
	EventDeployFailed,
	EventContainerStarted,
	EventContainerStopped,
	EventContainerDied,
	EventContainerOOM,
}

var notifiableSet = func() map[AppEventType]struct{} {
	m := make(map[AppEventType]struct{}, len(NotifiableEvents))
	for _, t := range NotifiableEvents {
		m[t] = struct{}{}
	}
	return m
}()

// IsNotifiable reports whether an event type may trigger outbound notifications.
func IsNotifiable(t AppEventType) bool {
	_, ok := notifiableSet[t]
	return ok
}

// NotificationChannelType identifies a notification transport. Telegram ships
// first; the model is generic so Slack/Discord/email can follow.
type NotificationChannelType string

const (
	ChannelTelegram NotificationChannelType = "telegram"
	ChannelSlack    NotificationChannelType = "slack"
	ChannelDiscord  NotificationChannelType = "discord"
)

// Config keys used by the channel types.
const (
	ConfigBotToken   = "bot_token"   // telegram; encrypted at rest
	ConfigChatID     = "chat_id"     // telegram
	ConfigWebhookURL = "webhook_url" // slack/discord; encrypted at rest
)

// NotificationChannel is a workspace-configured outbound channel that pushes a human-readable
// message when a subscribed event fires. Transport settings live in Config; secret values are
// encrypted at rest and masked in API responses.
type NotificationChannel struct {
	ID          uint                    `json:"id" gorm:"primaryKey"`
	WorkspaceID uint                    `json:"workspace_id" gorm:"index;not null"`
	Type        NotificationChannelType `json:"type" gorm:"not null"`
	Name        string                  `json:"name"`
	Config      map[string]string       `json:"config" gorm:"serializer:json"`
	Events      []string                `json:"events" gorm:"serializer:json"`
	Enabled     bool                    `json:"enabled" gorm:"not null;default:true"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
