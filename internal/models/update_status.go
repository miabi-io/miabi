// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// UpdateStatus caches the result of the daily release check. Exactly one row (ID 1) ever
// exists: it is platform state, not a user-editable setting, so it deliberately lives outside
// the `settings` table, whose every row is listed and editable on the admin page.
type UpdateStatus struct {
	ID uint `json:"-" gorm:"primaryKey"`
	// LatestVersion is the newest release for the running build's channel, as a
	// semver tag with the leading "v" (e.g. "v1.0.0-beta.5"). Empty until the
	// first successful check.
	LatestVersion string     `json:"latest_version"`
	ReleaseURL    string     `json:"release_url"`
	PublishedAt   *time.Time `json:"published_at"`
	// ETag from the last successful GitHub response. Replayed as If-None-Match so
	// an unchanged release list answers 304, which costs no API quota.
	ETag string `json:"-"`
	// CheckedVersion is the running build that produced LatestVersion. The ETag fingerprints only
	// the release list, so after an upgrade GitHub answers 304 and a verdict computed for the OLD
	// build would stick. Storing it lets Check notice the build moved and redo the comparison.
	CheckedVersion string `json:"-"`
	// CheckedAt is the last *attempt*; LastError explains a failing one. Both are
	// admin-visible so a silently broken checker cannot masquerade as "up to date".
	CheckedAt *time.Time `json:"checked_at"`
	LastError string     `json:"last_error,omitempty"`
	// DismissedVersion is the version an admin chose to stop being notified about.
	// Platform-wide: only platform admins ever see the notice.
	DismissedVersion string    `json:"dismissed_version,omitempty"`
	UpdatedAt        time.Time `json:"-"`
}
