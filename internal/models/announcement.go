// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// AnnouncementAudience selects who a broadcast reaches. Miabi is multi-tenant, so
// "everyone" is rarely the right blast radius: a maintenance window concerns the
// workspaces it affects, a licensing change the people who can act on it.
type AnnouncementAudience string

const (
	// AudienceAll reaches every active user.
	AudienceAll AnnouncementAudience = "all"
	// AudienceAdmins reaches the platform super-admins only.
	AudienceAdmins AnnouncementAudience = "admins"
	// AudienceOwners reaches users who own or administer at least one workspace —
	// the people who can act on a platform change.
	AudienceOwners AnnouncementAudience = "owners"
	// AudienceWorkspaces reaches the members of the workspaces listed in WorkspaceIDs.
	AudienceWorkspaces AnnouncementAudience = "workspaces"
)

// Valid reports whether a is a known audience.
func (a AnnouncementAudience) Valid() bool {
	switch a {
	case AudienceAll, AudienceAdmins, AudienceOwners, AudienceWorkspaces:
		return true
	}
	return false
}

// AnnouncementDismissal decides how a pinned notice retires for the reader. It is
// one field rather than a pair of flags so the combinations that mean nothing
// cannot be expressed, and so a further presentation can be added as a value.
type AnnouncementDismissal string

const (
	DismissOnce  AnnouncementDismissal = "once"
	DismissNever AnnouncementDismissal = "never"
)

// Valid reports whether d is a known dismissal mode.
func (d AnnouncementDismissal) Valid() bool {
	switch d {
	case DismissOnce, DismissNever:
		return true
	}
	return false
}

// AnnouncementStatus is the derived lifecycle position shown in the admin list.
type AnnouncementStatus string

const (
	AnnouncementScheduled AnnouncementStatus = "scheduled"
	AnnouncementPublished AnnouncementStatus = "published"
	AnnouncementExpired   AnnouncementStatus = "expired"
)

// Announcement is a platform notice written by an administrator and delivered to
// each recipient's inbox as a Notification. It is kept separately from the
// deliveries it produced so the operator can still see, edit or retract what was
// sent — none of which a pile of per-user rows allows.
type Announcement struct {
	ID    uint   `json:"id" gorm:"primaryKey"`
	Title string `json:"title" gorm:"not null"`
	// Message is deliberately not named Body: okapi resolves a request's body by
	// looking for a field named Body, and applies that rule again to the body's own
	// fields — a payload containing "body" binds as an empty struct, silently.
	Message    string        `json:"message"`
	Link       string        `json:"link,omitempty"`
	ActionText string        `json:"action_text,omitempty"`
	Severity   AlertSeverity `json:"severity" gorm:"not null;default:info"`

	Audience AnnouncementAudience `json:"audience" gorm:"not null;default:all"`
	// WorkspaceIDs narrows AudienceWorkspaces; ignored for every other audience.
	WorkspaceIDs []uint `json:"workspace_ids,omitempty" gorm:"serializer:json"`

	// Pinned raises the notice to a banner across the app instead of leaving it to
	// rest in the bell. Reserve it for notices that change what a user should do
	// right now. How the banner goes away is Dismissal's business.
	Pinned bool `json:"pinned" gorm:"not null;default:false"`
	// Dismissal governs the banner's close control; it means nothing unless Pinned.
	Dismissal AnnouncementDismissal `json:"dismissal" gorm:"not null;default:once"`

	// PublishAt schedules the broadcast; nil means it went out on creation.
	PublishAt *time.Time `json:"publish_at,omitempty"`
	// ExpiresAt retires the notice: the banner stops rendering and the inbox rows
	// stop counting as open. A maintenance window that has passed should not need
	// an operator to come back and clean it up.
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`

	// Recipients is how many inbox rows the broadcast has produced so far. It grows
	// as SyncUser backfills users who joined after the broadcast.
	Recipients int    `json:"recipients" gorm:"not null;default:0"`
	CreatedBy  uint   `json:"created_by" gorm:"not null"`
	AuthorName string `json:"author_name"`

	CreatedAt time.Time `json:"created_at" gorm:"index"`
	UpdatedAt time.Time `json:"updated_at"`

	// Status is derived, not stored; it is what the admin list renders.
	Status AnnouncementStatus `json:"status" gorm:"-"`
}

// Live reports whether the announcement is published and not yet expired — the
// state in which it may still be delivered to a user who has not received it.
func (a *Announcement) Live(now time.Time) bool {
	if a.PublishedAt == nil || a.PublishedAt.After(now) {
		return false
	}
	return a.ExpiresAt == nil || a.ExpiresAt.After(now)
}

// State derives the announcement's lifecycle position at now.
func (a *Announcement) State(now time.Time) AnnouncementStatus {
	switch {
	case a.PublishedAt == nil:
		return AnnouncementScheduled
	case a.ExpiresAt != nil && !a.ExpiresAt.After(now):
		return AnnouncementExpired
	default:
		return AnnouncementPublished
	}
}
