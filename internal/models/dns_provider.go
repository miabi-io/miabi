// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// DNS provider types Miabi can connect to. Each maps to a libdns module; the
// credential shape differs per type (see the dns package). "manual" is the
// implicit default for a domain with no connected provider (copy-paste flow).
const (
	DNSProviderCloudflare   = "cloudflare"
	DNSProviderRoute53      = "route53"
	DNSProviderDigitalOcean = "digitalocean"
)

// DNSProviderStatus is the last-known health of a connection.
const (
	DNSProviderStatusOK    = "ok"
	DNSProviderStatusError = "error"
)

// ValidDNSProviderType reports whether t is a known provider type.
//
// Deprecated: the dnscatalog package is the source of truth; call dnscatalog.Get.
func ValidDNSProviderType(t string) bool {
	switch t {
	case DNSProviderCloudflare, DNSProviderRoute53, DNSProviderDigitalOcean:
		return true
	default:
		return false
	}
}

// DNSProvider is a workspace's connection to a DNS host. Credentials are an opaque JSON blob
// whose shape depends on Type (Cloudflare token; Route 53 key/secret/region; DigitalOcean
// token), encrypted at rest and never returned by the API after create.
type DNSProvider struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_dnsprov_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace.
	Name string `json:"name" gorm:"index:idx_dnsprov_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string `json:"display_name"`
	Type        string `json:"type" gorm:"not null"` // cloudflare | route53 | digitalocean

	// CredentialsEnc is crypto.Encrypt(JSON-of-credentials); write-only — never
	// serialized to clients (json:"-").
	CredentialsEnc string `json:"-" gorm:"type:text"`

	// Status / LastError record the last connection test or reconcile result.
	Status    string `json:"status" gorm:"not null;default:ok"` // ok | error
	LastError string `json:"last_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
