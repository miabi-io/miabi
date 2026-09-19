// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Organization is the tenant boundary: it owns workspaces, caps how many, and may own the clusters
// they deploy to. It is also the identity realm SSO, SAML, enforced login and SCIM attach to. Miabi
// seeds one default organization; every nullable organization_id resolves to it.
//
// It is deliberately NOT a data-isolation boundary — every resource is scoped by workspace_id in the
// repository layer, and that stays the isolation mechanism.
type Organization struct {
	UIDModel
	ID uint `json:"id" gorm:"primaryKey"`
	// Name is the globally unique handle (e.g. "default"). Immutable once created: it is the handle
	// admins and the API address the org by.
	Name string `json:"name" gorm:"uniqueIndex;not null"`
	// DisplayName is the free-text label shown in the UI.
	DisplayName string `json:"display_name"`
	// IsDefault marks the org new workspaces, users and providers attach to when they name none.
	// Exactly one row carries it, enforced by a partial unique index.
	IsDefault bool `json:"is_default" gorm:"default:false;not null"`
	// OwnerUserID is the user accountable for the org; 0 when none is assigned.
	OwnerUserID uint `json:"owner_user_id" gorm:"index"`
	// MaxWorkspaces caps how many workspaces the org may hold. Unlimited (-1) means no cap and 0
	// means none allowed, matching Plan's convention.
	MaxWorkspaces int `json:"max_workspaces" gorm:"not null;default:-1"`
	// DefaultClusterID is the location the org's new workspaces default to; nil leaves them on the
	// platform default. An org that owns clusters is confined to them (see Cluster.OrganizationID).
	DefaultClusterID *uint `json:"default_cluster_id,omitempty" gorm:"index"`
	// EnforceSSO disables local password login for this org's users (Enterprise; writing it is gated
	// on the sso_saml entitlement). Off by default.
	EnforceSSO bool      `json:"enforce_sso" gorm:"default:false;not null"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`

	// WorkspaceCount is filled on read for the admin list; never persisted.
	WorkspaceCount int64 `json:"workspace_count" gorm:"-"`
}

// DefaultOrganizationName is the handle of the seeded default organization.
const DefaultOrganizationName = "default"

// WorkspacesUnlimited reports whether the org has no workspace cap.
func (o *Organization) WorkspacesUnlimited() bool { return o != nil && o.MaxWorkspaces < 0 }
