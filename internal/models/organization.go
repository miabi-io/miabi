// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Organization is a realm owning identity configuration — SSO, SAML, enforced login, SCIM.
// Miabi seeds a single default organization spanning all workspaces. The table and nullable
// foreign keys exist from day one so multi-org needs no destructive migration.
type Organization struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// Name is the globally unique handle (e.g. "default").
	Name string `json:"name" gorm:"uniqueIndex;not null"`
	// DisplayName is the free-text label shown in the UI.
	DisplayName string `json:"display_name"`
	// IsDefault marks the org new workspaces and providers attach to. Exactly one
	// row is the default.
	IsDefault bool `json:"is_default" gorm:"default:false;not null"`
	// EnforceSSO disables local password login for this org's users (Enterprise;
	// writing it is gated on the sso_saml entitlement). Off by default.
	EnforceSSO bool      `json:"enforce_sso" gorm:"default:false;not null"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DefaultOrganizationName is the handle of the seeded default organization.
const DefaultOrganizationName = "default"
