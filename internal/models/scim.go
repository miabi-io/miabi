// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// SCIMToken is a bearer credential an identity provider uses to call the SCIM 2.0 endpoint.
// Only the SHA-256 hash is stored; the plaintext is shown once at creation. Migrated in every
// build but only used by the enterprise SCIM handler (gated on the scim entitlement).
type SCIMToken struct {
	ID             uint       `json:"id" gorm:"primaryKey"`
	OrganizationID *uint      `json:"organization_id" gorm:"index"`
	Name           string     `json:"name" gorm:"not null"`
	TokenHash      string     `json:"-" gorm:"uniqueIndex;not null"`
	LastUsedAt     *time.Time `json:"last_used_at"`
	CreatedAt      time.Time  `json:"created_at"`
}
