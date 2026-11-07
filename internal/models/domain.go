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

type DomainTLSMode string

const (
	// DomainTLSACME serves certificates from Goma's ACME (Let's Encrypt) issuer.
	DomainTLSACME DomainTLSMode = "acme"
	// DomainTLSCustom expects a user-supplied certificate.
	DomainTLSCustom DomainTLSMode = "custom"
)

// VerifiedVia distinguishes proof from waiver: only a DNS proof can go stale and be revoked by the drift cron.
type VerifiedVia string

const (
	// VerifiedViaDNS is ownership proven by the challenge TXT record.
	VerifiedViaDNS         VerifiedVia = "dns"
	VerifiedViaDNSProvider VerifiedVia = "dns_provider"
	// A waiver for private or unreachable DNS, not a proof of ownership.
	VerifiedViaAdmin VerifiedVia = "admin"
)

func (v VerifiedVia) IsWaiver() bool { return v == VerifiedViaAdmin }

// Domain tracks DNS-verified ownership and default TLS policy; distinct from the declarative Route kind.
type Domain struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index:idx_domain_workspace_name,unique;not null"`
	Name        string `json:"name" gorm:"index:idx_domain_workspace_name,unique;not null"` // e.g. example.com

	// TLSMode is the default certificate policy for routes under this domain.
	TLSMode DomainTLSMode `json:"tls_mode" gorm:"not null;default:acme"`
	// Wildcard marks the domain as covering *.name as well as name.
	Wildcard bool `json:"wildcard" gorm:"not null;default:false"`

	// VerificationToken is published as a TXT record and is not a secret.
	Verified          bool       `json:"verified" gorm:"not null;default:false"`
	VerifiedAt        *time.Time `json:"verified_at,omitempty"`
	VerificationToken string     `json:"verification_token" gorm:"not null"`
	// VerifiedVia records how ownership was established
	VerifiedVia VerifiedVia `json:"verified_via" gorm:"not null;default:dns"`

	// Lets the drift cron count consecutive misses before un-verifying a domain.
	VerificationCheckedAt *time.Time `json:"verification_checked_at,omitempty"`
	VerificationError     string     `json:"verification_error,omitempty"`
	// The drift cron un-verifies only past a threshold, absorbing transient DNS blips.
	VerificationMisses int `json:"-" gorm:"not null;default:0"`

	// Banned domains are never served or verifiable regardless of ownership; set by a platform admin.
	Banned    bool       `json:"banned" gorm:"not null;default:false"`
	BannedAt  *time.Time `json:"banned_at,omitempty"`
	BanReason string     `json:"ban_reason,omitempty"`

	// nil means manual: the user adds DNS records by hand.
	DNSProviderID *uint        `json:"dns_provider_id,omitempty" gorm:"index"`
	DNSProvider   *DNSProvider `json:"-" gorm:"foreignKey:DNSProviderID;constraint:OnDelete:SET NULL"`

	ServingUnverified bool `json:"serving_unverified,omitempty" gorm:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DomainStatus is derived, not stored: "failed" means a check ran and did not prove ownership.
type DomainStatus string

const (
	DomainStatusPending  DomainStatus = "pending"
	DomainStatusVerified DomainStatus = "verified"
	DomainStatusFailed   DomainStatus = "failed"
	DomainStatusBanned   DomainStatus = "banned"
)

// A ban takes precedence over verification since it overrides serving entirely.
func (d *Domain) Status() DomainStatus {
	switch {
	case d.Banned:
		return DomainStatusBanned
	case d.Verified:
		return DomainStatusVerified
	case d.VerificationError != "":
		return DomainStatusFailed
	default:
		return DomainStatusPending
	}
}

// ChallengeHost is the DNS name where the ownership TXT record must be added.
func (d *Domain) ChallengeHost() string {
	return "_miabi-challenge." + d.Name
}

// ChallengeValue is the expected TXT record value proving ownership.
func (d *Domain) ChallengeValue() string {
	return "miabi-verification=" + d.VerificationToken
}
