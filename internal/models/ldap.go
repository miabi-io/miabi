// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// LDAP TLS modes for the directory connection.
const (
	LDAPTLSNone     = "none"     // plain LDAP (discouraged; dev/LAN only)
	LDAPTLSStartTLS = "starttls" // upgrade a plain connection with StartTLS
	LDAPTLSLDAPS    = "ldaps"    // implicit TLS (usually port 636)
)

// LDAPConfig is an LDAP / Active Directory connection for an organization. Migrated in every
// build but only read by the enterprise LDAP authenticator and admin handler (gated on
// sso_ldap); empty in Community. Mirrors SAMLConfig's per-org shape.
type LDAPConfig struct {
	ID             uint  `json:"id" gorm:"primaryKey"`
	OrganizationID *uint `json:"organization_id" gorm:"index"`
	// Name is the globally unique handle (slug); DisplayName is the free-text label.
	Name        string `json:"name" gorm:"uniqueIndex;not null"`
	DisplayName string `json:"display_name"`

	// Connection.
	Host            string `json:"host" gorm:"not null"`
	Port            int    `json:"port" gorm:"not null;default:389"`
	TLSMode         string `json:"tls_mode" gorm:"not null;default:'starttls'"` // none | starttls | ldaps
	CACertPEM       string `json:"ca_cert_pem" gorm:"type:text"`                // optional private-CA trust
	InsecureSkipTLS bool   `json:"insecure_skip_tls" gorm:"not null;default:false"`
	TimeoutSeconds  int    `json:"timeout_seconds" gorm:"not null;default:10"`

	// Bind — the service account used to search for the user entry before
	// re-binding as the user to verify the password.
	BindDN string `json:"bind_dn"`
	// BindPasswordEnc is the service-account password, encrypted at rest (crypto
	// package). Never serialized; BindPasswordSet exposes only whether it is set.
	BindPasswordEnc string `json:"-" gorm:"type:text"`

	// User search.
	UserBaseDN   string `json:"user_base_dn"`
	UserFilter   string `json:"user_filter"`   // %s = escaped identifier, e.g. (sAMAccountName=%s) or (uid=%s)
	AttrEmail    string `json:"attr_email"`    // mail | userPrincipalName (blank → "mail")
	AttrName     string `json:"attr_name"`     // displayName | cn (blank → "displayName")
	AttrUsername string `json:"attr_username"` // sAMAccountName | uid → User.Username

	// Group resolution (optional; drives access via LDAPGroupMapping).
	GroupBaseDN  string `json:"group_base_dn"`
	GroupFilter  string `json:"group_filter"`  // %s = user DN, e.g. (member=%s); blank → use MemberAttr
	MemberAttr   string `json:"member_attr"`   // memberOf (read from the user entry) — AD default
	NestedGroups bool   `json:"nested_groups"` // AD: expand nested groups (LDAP_MATCHING_RULE_IN_CHAIN)

	Enabled   bool      `json:"enabled" gorm:"not null;default:true"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Mappings are loaded on demand (not embedded in list responses).
	Mappings []LDAPGroupMapping `json:"mappings,omitempty" gorm:"foreignKey:LDAPConfigID"`

	// BindPasswordSet is a read-only flag for the API (whether a service-account
	// password is stored) so the secret itself is never exposed. Not persisted;
	// populated by the repository on read.
	BindPasswordSet bool `json:"bind_password_set" gorm:"-"`
}

// LDAPGroupMapping maps a directory group to Miabi access, reconciled on every login:
// SystemAdmin grants the platform-admin role, a WorkspaceID + WorkspaceRole grants
// membership. A mapping with neither is a no-op, kept for documentation.
type LDAPGroupMapping struct {
	ID           uint `json:"id" gorm:"primaryKey"`
	LDAPConfigID uint `json:"ldap_config_id" gorm:"index;not null"`
	// GroupDN is matched (case-insensitively) against the user's resolved group
	// DNs; a bare CN is also accepted for convenience.
	GroupDN     string `json:"group_dn" gorm:"not null"`
	SystemAdmin bool   `json:"system_admin" gorm:"not null;default:false"`
	// WorkspaceID + WorkspaceRole grant workspace membership (nil = no grant).
	WorkspaceID   *uint         `json:"workspace_id"`
	WorkspaceRole WorkspaceRole `json:"workspace_role"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}
