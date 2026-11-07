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

// SAMLConfig is migrated in every build but only used by the enterprise SAML handler (sso_saml); empty in Community.
type SAMLConfig struct {
	ID             uint  `json:"id" gorm:"primaryKey"`
	OrganizationID *uint `json:"organization_id" gorm:"index"`
	// Name is the globally unique handle.
	Name string `json:"name" gorm:"uniqueIndex;not null"`
	// DisplayName is the free-text label shown in the UI.
	DisplayName string `json:"display_name"`

	// IdP metadata: a URL to fetch, or inline XML. One of the two is required.
	IDPMetadataURL string `json:"idp_metadata_url"`
	IDPMetadataXML string `json:"idp_metadata_xml" gorm:"type:text"`

	// Defaults to the SP metadata URL when blank.
	SPEntityID string `json:"sp_entity_id"`

	// Blank falls back to "email" / "displayName" (and the NameID for email).
	AttrEmail string `json:"attr_email"`
	AttrName  string `json:"attr_name"`

	Enabled   bool      `json:"enabled" gorm:"default:true;not null"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
