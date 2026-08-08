// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// DomainTLSMode is the default certificate policy for routes served under a
// domain.
type DomainTLSMode string

const (
	// DomainTLSACME serves certificates from Goma's ACME (Let's Encrypt) issuer.
	DomainTLSACME DomainTLSMode = "acme"
	// DomainTLSCustom expects a user-supplied certificate.
	DomainTLSCustom DomainTLSMode = "custom"
)

// Domain is a hostname (or zone) a workspace owns, tracking DNS-verified ownership and the
// default TLS policy; routes bind hostnames resolving under a verified domain. Distinct from
// the declarative Route kind, which is an HTTP routing rule.
type Domain struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index:idx_domain_workspace_name,unique;not null"`
	Name        string `json:"name" gorm:"index:idx_domain_workspace_name,unique;not null"` // e.g. example.com

	// TLSMode is the default certificate policy for routes under this domain.
	TLSMode DomainTLSMode `json:"tls_mode" gorm:"not null;default:acme"`
	// Wildcard marks the domain as covering *.name as well as name.
	Wildcard bool `json:"wildcard" gorm:"not null;default:false"`

	// Verified records DNS-proven ownership. VerificationToken is published by the
	// user as a TXT record; it is not a secret (it is meant to be public).
	Verified          bool       `json:"verified" gorm:"not null;default:false"`
	VerifiedAt        *time.Time `json:"verified_at,omitempty"`
	VerificationToken string     `json:"verification_token" gorm:"not null"`

	// VerificationCheckedAt records the last ownership check (successful or not) and
	// VerificationError the last failure reason. Together they give the UI a verification history
	// and let the drift cron count consecutive misses before un-verifying a domain.
	VerificationCheckedAt *time.Time `json:"verification_checked_at,omitempty"`
	VerificationError     string     `json:"verification_error,omitempty"`
	// VerificationMisses counts consecutive failed re-verifications of an
	// already-verified domain; the drift cron flips Verified off once it crosses a
	// threshold, absorbing transient DNS blips.
	VerificationMisses int `json:"-" gorm:"not null;default:0"`

	// Banned blocks a domain platform-wide: a banned domain is never served by the
	// gateway (its routes are forced offline) and cannot be verified, regardless of
	// ownership or workspace privilege. Set by a platform admin (e.g. for abuse).
	Banned    bool       `json:"banned" gorm:"not null;default:false"`
	BannedAt  *time.Time `json:"banned_at,omitempty"`
	BanReason string     `json:"ban_reason,omitempty"`

	// DNSProviderID optionally links the domain to a connected DNS provider so
	// Miabi automates the ownership TXT (and, later, app A/AAAA) records. nil =
	// manual: the user adds DNS records by hand (today's copy-paste flow).
	DNSProviderID *uint `json:"dns_provider_id,omitempty" gorm:"index"`
	// DNSProvider declares the FK so deleting the provider nulls this link
	// (ON DELETE SET NULL) — the domain reverts to manual DNS — instead of
	// dangling. Association not serialized.
	DNSProvider *DNSProvider `json:"-" gorm:"foreignKey:DNSProviderID;constraint:OnDelete:SET NULL"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DomainStatus is a presentational verification state derived from the domain's
// verification fields (not stored): "verified", "failed" (a check ran and did not
// prove ownership), or "pending" (no successful check yet).
type DomainStatus string

const (
	DomainStatusPending  DomainStatus = "pending"
	DomainStatusVerified DomainStatus = "verified"
	DomainStatusFailed   DomainStatus = "failed"
	DomainStatusBanned   DomainStatus = "banned"
)

// Status derives the domain's presentational state for the UI. A ban takes
// precedence over verification, since it overrides serving entirely.
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
