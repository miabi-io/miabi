// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Components an UpdateStatus row may track.
const (
	UpdateComponentMiabi = "miabi"
	UpdateComponentAgent = "agent"
)

// UpdateStatus caches the result of a daily release check.
type UpdateStatus struct {
	ID               uint       `json:"-" gorm:"primaryKey"`
	Component        string     `json:"component" gorm:"uniqueIndex;not null;default:miabi"`
	LatestVersion    string     `json:"latest_version"`
	ReleaseURL       string     `json:"release_url"`
	PublishedAt      *time.Time `json:"published_at"`
	ETag             string     `json:"-"`
	CheckedVersion   string     `json:"-"`
	CheckedAt        *time.Time `json:"checked_at"`
	LastError        string     `json:"last_error,omitempty"`
	DismissedVersion string     `json:"dismissed_version,omitempty"`
	UpdatedAt        time.Time  `json:"-"`
}
