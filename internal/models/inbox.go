// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Notification is a per-user inbox item — the delivery of an Alert (or a
// standalone info message) to one member. Alerts are workspace-level and shared;
// notifications are per-user so read/unread stays clean. It renders the alert at
// delivery time (Title/Body/link), so the inbox is self-contained even after the
// alert archives. When an alert updates (crash-loop count bump, auto-resolve),
// the existing notification is updated in place (unique per user+alert) rather
// than a new row spamming the bell.
type Notification struct {
	ID          uint          `json:"id" gorm:"primaryKey"`
	UserID      uint          `json:"user_id" gorm:"index:idx_notif_user_alert,priority:1;not null"`
	WorkspaceID uint          `json:"workspace_id" gorm:"index;not null"`
	AlertID     *uint         `json:"alert_id,omitempty" gorm:"index:idx_notif_user_alert,priority:2"`
	Kind        string        `json:"kind" gorm:"not null;default:alert"` // alert | info
	Category    AlertCategory `json:"category"`
	Severity    AlertSeverity `json:"severity" gorm:"not null;default:info"`
	Title       string        `json:"title"`
	Body        string        `json:"body"`
	SubjectLink string        `json:"subject_link,omitempty"`
	// ReadAt is nil while unread. Read state is per-user.
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at" gorm:"index"`
	UpdatedAt time.Time  `json:"updated_at"`
}
