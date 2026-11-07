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

// Webhook's signing Secret is encrypted at rest, returned in cleartext once, then only as HasSecret.
type Webhook struct {
	ID          uint              `json:"id" gorm:"primaryKey"`
	WorkspaceID uint              `json:"workspace_id" gorm:"index;not null"`
	Name        string            `json:"name"`
	URL         string            `json:"url" gorm:"not null"`
	Secret      string            `json:"-" gorm:"not null"`
	Events      []string          `json:"events" gorm:"serializer:json"`
	Headers     map[string]string `json:"headers,omitempty" gorm:"serializer:json"`
	Enabled     bool              `json:"enabled" gorm:"not null;default:true"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// HasSecret is a transient flag for responses (never persisted).
	HasSecret bool `json:"has_secret" gorm:"-"`
	// PlainSecret carries the generated secret in the create response only.
	PlainSecret string `json:"secret,omitempty" gorm:"-"`
}

// WebhookDeliveryStatus is the terminal outcome of a delivery attempt chain.
type WebhookDeliveryStatus string

const (
	WebhookDeliverySuccess WebhookDeliveryStatus = "success"
	WebhookDeliveryFailed  WebhookDeliveryStatus = "failed"
)

// One row per attempt; Attempt is 1-based.
type WebhookDelivery struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WebhookID   uint   `json:"webhook_id" gorm:"index;not null"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index;not null"`
	Event       string `json:"event" gorm:"not null"`
	// EventID is the source AppEvent, enabling exact redelivery. 0 for tests.
	EventID        uint                  `json:"event_id,omitempty"`
	Status         WebhookDeliveryStatus `json:"status" gorm:"type:varchar(16);not null;index"`
	HTTPStatusCode int                   `json:"http_status_code"`
	ErrorMessage   string                `json:"error_message,omitempty"`
	Attempt        int                   `json:"attempt" gorm:"not null;default:1"`
	CreatedAt      time.Time             `json:"created_at"`
}
