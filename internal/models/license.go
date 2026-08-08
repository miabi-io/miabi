// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// License is the single active commercial license row. The signed Token is the source of
// truth, re-verified against the embedded public key on every load, so the parsed columns are
// only a display cache. Migrated in every build but empty and unused in Community.
type License struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	LicenseID   string    `json:"license_id" gorm:"uniqueIndex;not null"`
	Edition     string    `json:"edition" gorm:"not null"`
	Token       string    `json:"-" gorm:"type:text;not null"` // never serialized to the API
	Customer    string    `json:"customer"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	GraceDays   int       `json:"grace_days"`
	InstalledBy uint      `json:"installed_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
