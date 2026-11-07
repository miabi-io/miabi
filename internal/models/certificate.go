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

// Certificate sources.
const (
	// CertSourceImported is a bring-your-own certificate the user uploaded.
	CertSourceImported = "imported"
	CertSourceACME     = "acme"
)

// Managed-certificate (ACME) statuses.
const (
	CertStatusActive  = "active"  // issued and in use
	CertStatusIssuing = "issuing" // issuance in progress
	CertStatusFailed  = "failed"  // last issuance/renewal failed (see LastError)
)

// The PEM and key are sourced server-side at render time; KeyEnc is encrypted and never returned.
type Certificate struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_cert_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_cert_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	CertPEM     string `json:"-" gorm:"type:text"` // leaf + intermediates (PEM)
	KeyEnc      string `json:"-" gorm:"type:text"` // private key, encrypted at rest

	// Parsed metadata (populated on import / replace / issue).
	CommonName string    `json:"common_name"`
	DNSNames   []string  `json:"dns_names" gorm:"serializer:json"` // SANs, for host auto-match
	Issuer     string    `json:"issuer"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
	SerialHex  string    `json:"serial_hex"`

	// Managed (ACME) fields; apply only to "acme" certs (Source defaults to "imported").
	Source        string `json:"source" gorm:"not null;default:imported"` // imported | acme
	DNSProviderID *uint  `json:"dns_provider_id,omitempty" gorm:"index"`  // provider used to issue
	AutoRenew     bool   `json:"auto_renew" gorm:"not null;default:false"`
	Status        string `json:"status" gorm:"not null;default:active"` // active | issuing | failed
	LastError     string `json:"last_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
