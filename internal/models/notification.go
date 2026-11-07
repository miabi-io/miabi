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

package models

import "time"

// NotifiableEvents excludes config changes and routine DB transitions: only outcomes worth waking someone for.
var NotifiableEvents = []AppEventType{
	EventDeployStarted,
	EventDeploySucceeded,
	EventDeployFailed,
	EventContainerStarted,
	EventContainerStopped,
	EventContainerDied,
	EventContainerOOM,
	EventContainerRemoved,
	EventDriftDetected,
	EventDriftResolved,
	EventReconcileRedeploy,
	EventReconcileBreakerOpen,
	EventDatabaseProvisioned,
	EventDatabaseProvisionFailed,
	EventDatabaseUpgraded,
	EventDatabaseUpgradeFailed,
	EventDatabaseBackupSucceeded,
	EventDatabaseBackupFailed,
	EventDatabaseRestoreSucceeded,
	EventDatabaseRestoreFailed,
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

// NotificationChannel secret Config values are encrypted at rest and masked in API responses.
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
