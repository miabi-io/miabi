// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Webhook is a workspace-registered HTTP endpoint receiving a signed JSON payload when a
// subscribed application event fires. The signing Secret is encrypted at rest and never
// serialized (a transient HasSecret flag instead); it is returned in cleartext once.
type Webhook struct {
	ID          uint     `json:"id" gorm:"primaryKey"`
	WorkspaceID uint     `json:"workspace_id" gorm:"index;not null"`
	Name        string   `json:"name"`
	URL         string   `json:"url" gorm:"not null"`
	Secret      string   `json:"-" gorm:"not null"`
	Events      []string `json:"events" gorm:"serializer:json"`
	// Headers are extra HTTP headers sent with each delivery (e.g. an
	// Authorization header for the receiving service).
	Headers map[string]string `json:"headers,omitempty" gorm:"serializer:json"`
	Enabled bool              `json:"enabled" gorm:"not null;default:true"`

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

// WebhookDelivery records the outcome of delivering one event to one webhook.
// One row is written per attempt; Attempt is the 1-based attempt number.
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
