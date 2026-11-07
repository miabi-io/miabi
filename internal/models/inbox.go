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

const (
	NotificationKindAlert        = "alert"
	NotificationKindInfo         = "info"
	NotificationKindAnnouncement = "announcement"
)

// Notification is a per-user delivery of a shared Alert or Announcement, updated in place rather than re-sent.
type Notification struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// WorkspaceID is 0 for platform-scoped items, which follow the user into every workspace.
	UserID         uint          `json:"user_id" gorm:"index:idx_notif_user_alert,priority:1;not null"`
	WorkspaceID    uint          `json:"workspace_id" gorm:"index;not null"`
	AlertID        *uint         `json:"alert_id,omitempty" gorm:"index:idx_notif_user_alert,priority:2"`
	AnnouncementID *uint         `json:"announcement_id,omitempty" gorm:"index"`
	Kind           string        `json:"kind" gorm:"not null;default:alert"` // alert | info | announcement
	Category       AlertCategory `json:"category"`
	Severity       AlertSeverity `json:"severity" gorm:"not null;default:info"`
	Title          string        `json:"title"`
	Body           string        `json:"body"`
	SubjectLink    string        `json:"subject_link,omitempty"`
	ActionText     string        `json:"action_text,omitempty"`

	// Denormalized from the announcement so the banner is a plain inbox query with no join.
	Pinned bool `json:"pinned" gorm:"not null;default:false"`
	// Denormalized so the banner stays a plain per-user query with no join.
	Dismissal AnnouncementDismissal `json:"dismissal" gorm:"not null;default:once"`
	// ExpiresAt retires the item from the banner without deleting the history.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Distinct from ReadAt: opening the bell reads a notice; dismissing means you are done with it.
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
	// ReadAt is nil while unread. Read state is per-user.
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at" gorm:"index"`
	UpdatedAt time.Time  `json:"updated_at"`
}
