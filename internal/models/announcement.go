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

// Multi-tenant, so "everyone" is rarely the right blast radius.
type AnnouncementAudience string

const (
	// AudienceAll reaches every active user.
	AudienceAll AnnouncementAudience = "all"
	// AudienceAdmins reaches the platform super-admins only.
	AudienceAdmins AnnouncementAudience = "admins"
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

// One field rather than flags so meaningless combinations cannot be expressed.
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

// Kept apart from its deliveries so the operator can still see, edit or retract what was sent.
type Announcement struct {
	ID    uint   `json:"id" gorm:"primaryKey"`
	Title string `json:"title" gorm:"not null"`
	// Not named Body: okapi binds a nested "body" field as an empty struct, silently.
	Message    string        `json:"message"`
	Link       string        `json:"link,omitempty"`
	ActionText string        `json:"action_text,omitempty"`
	Severity   AlertSeverity `json:"severity" gorm:"not null;default:info"`

	Audience AnnouncementAudience `json:"audience" gorm:"not null;default:all"`
	// WorkspaceIDs narrows AudienceWorkspaces; ignored for every other audience.
	WorkspaceIDs []uint `json:"workspace_ids,omitempty" gorm:"serializer:json"`

	// Reserve for notices that change what a user should do right now.
	Pinned bool `json:"pinned" gorm:"not null;default:false"`
	// Dismissal governs the banner's close control; it means nothing unless Pinned.
	Dismissal AnnouncementDismissal `json:"dismissal" gorm:"not null;default:once"`

	// PublishAt schedules the broadcast; nil means it went out on creation.
	PublishAt *time.Time `json:"publish_at,omitempty"`
	// A past maintenance window should not need an operator to clean it up.
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`

	// Grows as SyncUser backfills users who joined after the broadcast.
	Recipients int    `json:"recipients" gorm:"not null;default:0"`
	CreatedBy  uint   `json:"created_by" gorm:"not null"`
	AuthorName string `json:"author_name"`

	CreatedAt time.Time `json:"created_at" gorm:"index"`
	UpdatedAt time.Time `json:"updated_at"`

	// Status is derived, not stored; it is what the admin list renders.
	Status AnnouncementStatus `json:"status" gorm:"-"`
}

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
