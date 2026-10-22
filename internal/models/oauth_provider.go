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

// OAuthProviderType is the kind of identity provider.
type OAuthProviderType string

const (
	OAuthProviderGoogle OAuthProviderType = "google" // Google OAuth 2.0 (well-known discovery)
	OAuthProviderOIDC   OAuthProviderType = "oidc"   // generic OpenID Connect
)

// OAuthProvider is a platform-level SSO identity provider. Secrets are stored
// encrypted at rest (see internal/services/crypto) and never serialized.
type OAuthProvider struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// Name is the globally unique handle used in callback routes.
	Name string `json:"name" gorm:"uniqueIndex;not null"`
	// DisplayName is the free-text label shown on login buttons.
	DisplayName string            `json:"display_name"`
	Type        OAuthProviderType `json:"type" gorm:"not null"`

	ClientID        string `json:"-" gorm:"not null"`
	ClientSecretEnc string `json:"-" gorm:"column:client_secret;not null"`

	// OIDC endpoints. For "google" these are derived from the well-known config.
	Issuer      string `json:"issuer"`
	AuthURL     string `json:"auth_url"`
	TokenURL    string `json:"token_url"`
	UserInfoURL string `json:"userinfo_url"`

	Scopes string `json:"scopes" gorm:"default:'openid email profile'"`

	Enabled      bool `json:"enabled" gorm:"default:true;not null"`
	Hidden       bool `json:"hidden" gorm:"default:false;not null"`       // hide from login buttons
	AutoRegister bool `json:"auto_register" gorm:"default:true;not null"` // auto-create users on first login
	// AllowUnverifiedEmail links an existing account by an email the provider has not verified. Only for a
	// directory that never sends email_verified: on a public provider anyone can claim any address.
	AllowUnverifiedEmail bool `json:"allow_unverified_email" gorm:"default:false;not null"`

	// AllowedDomains is a CSV of email domains permitted to sign in / register
	// through this provider. Empty means any domain is allowed.
	AllowedDomains string `json:"allowed_domains"`

	// OrganizationID is the realm this provider belongs to (nullable → default org).
	OrganizationID *uint `json:"organization_id" gorm:"index"`

	// EmailClaim / NameClaim / UsernameClaim map a generic OIDC userinfo claim
	// onto the user's email / display name / handle. Empty falls back to the
	// standard "email" / "name" / "preferred_username".
	EmailClaim string `json:"email_claim"`
	NameClaim  string `json:"name_claim"`
	// UsernameClaim names the claim carrying the handle the user is known by at
	// the provider. Without it Miabi derives one from the email local part, so an
	// account whose directory handle differs from its mailbox gets the wrong one.
	UsernameClaim string `json:"username_claim"`

	// DefaultWorkspaceID + DefaultRole auto-join a newly registered SSO user to a
	// workspace with a role on first login. Nil = no auto-join (user is invited
	// separately).
	DefaultWorkspaceID *uint         `json:"default_workspace_id" gorm:"index"`
	DefaultRole        WorkspaceRole `json:"default_role"`

	// GroupsClaim names the userinfo claim holding the user's groups (Enterprise group mapping).
	// Empty turns group mapping off for this provider.
	GroupsClaim string `json:"groups_claim"`
	// GroupPrefix enables structured groups "<prefix>:<workspace>:<role>", each granting that role in
	// that workspace. Empty accepts only the explicit Mappings.
	GroupPrefix string              `json:"group_prefix"`
	Mappings    []OAuthGroupMapping `json:"mappings,omitempty" gorm:"foreignKey:ProviderID"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OAuthGroupMapping maps one provider group to Miabi access, applied at every sign-in like
// LDAPGroupMapping: platform admin, or a role in a fixed workspace.
type OAuthGroupMapping struct {
	ID         uint   `json:"id" gorm:"primaryKey"`
	ProviderID uint   `json:"provider_id" gorm:"index;not null"`
	Group      string `json:"group" gorm:"not null"`
	// SystemAdmin grants the platform admin role.
	SystemAdmin bool `json:"system_admin" gorm:"not null;default:false"`
	// WorkspaceID + WorkspaceRole grant workspace membership (nil = no grant).
	WorkspaceID   *uint         `json:"workspace_id"`
	WorkspaceRole WorkspaceRole `json:"workspace_role"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}
