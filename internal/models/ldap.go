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

// LDAP TLS modes for the directory connection.
const (
	LDAPTLSNone     = "none"     // plain LDAP (discouraged; dev/LAN only)
	LDAPTLSStartTLS = "starttls" // upgrade a plain connection with StartTLS
	LDAPTLSLDAPS    = "ldaps"    // implicit TLS (usually port 636)
)

// LDAPConfig is migrated in every build but only read by the enterprise LDAP code (sso_ldap); empty in Community.
type LDAPConfig struct {
	ID             uint  `json:"id" gorm:"primaryKey"`
	OrganizationID *uint `json:"organization_id" gorm:"index"`
	// Name is the globally unique handle (slug); DisplayName is the free-text label.
	Name        string `json:"name" gorm:"uniqueIndex;not null"`
	DisplayName string `json:"display_name"`

	Host            string `json:"host" gorm:"not null"`
	Port            int    `json:"port" gorm:"not null;default:389"`
	TLSMode         string `json:"tls_mode" gorm:"not null;default:'starttls'"` // none | starttls | ldaps
	CACertPEM       string `json:"ca_cert_pem" gorm:"type:text"`                // optional private-CA trust
	InsecureSkipTLS bool   `json:"insecure_skip_tls" gorm:"not null;default:false"`
	TimeoutSeconds  int    `json:"timeout_seconds" gorm:"not null;default:10"`

	// Service account used to search for the user entry before re-binding as the user.
	BindDN string `json:"bind_dn"`
	// Never serialized; BindPasswordSet exposes only whether it is set.
	BindPasswordEnc string `json:"-" gorm:"type:text"`

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

	BindPasswordSet bool `json:"bind_password_set" gorm:"-"`
}

// LDAPGroupMapping is reconciled on every login; a mapping granting neither admin nor membership is a no-op.
type LDAPGroupMapping struct {
	ID           uint `json:"id" gorm:"primaryKey"`
	LDAPConfigID uint `json:"ldap_config_id" gorm:"index;not null"`
	// Matched case-insensitively against resolved group DNs; a bare CN is also accepted.
	GroupDN     string `json:"group_dn" gorm:"not null"`
	SystemAdmin bool   `json:"system_admin" gorm:"not null;default:false"`
	// WorkspaceID + WorkspaceRole grant workspace membership (nil = no grant).
	WorkspaceID   *uint         `json:"workspace_id"`
	WorkspaceRole WorkspaceRole `json:"workspace_role"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}
