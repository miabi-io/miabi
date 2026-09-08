// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Notification kinds. An inbox item is either the delivery of an Alert, a
// platform announcement, or a standalone informational note.
const (
	NotificationKindAlert        = "alert"
	NotificationKindInfo         = "info"
	NotificationKindAnnouncement = "announcement"
)

// Notification is a per-user inbox item — the delivery of an Alert or an Announcement to one
// member. Alerts are workspace-level and shared; notifications are per-user so read/unread stays
// clean. It renders the source at delivery time and updates in place rather than spamming the bell.
type Notification struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// WorkspaceID is 0 for platform-scoped items (announcements), which follow the
	// user into every workspace they open rather than belonging to any one of them.
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

	// Pinned raises the item to an app-wide banner until dismissed. Denormalized
	// from the announcement so the banner is a plain inbox query with no join.
	Pinned bool `json:"pinned" gorm:"not null;default:false"`
	// Dismissal is denormalized from the announcement, for the same reason Pinned
	// is: the banner stays a plain per-user query with no join.
	Dismissal AnnouncementDismissal `json:"dismissal" gorm:"not null;default:once"`
	// ExpiresAt retires the item from the banner without deleting the history.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// DismissedAt hides the item from the banner. Distinct from ReadAt: opening the
	// bell reads a notice, deciding you are done with it dismisses it.
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
	// ReadAt is nil while unread. Read state is per-user.
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at" gorm:"index"`
	UpdatedAt time.Time  `json:"updated_at"`
}
