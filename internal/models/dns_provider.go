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

import (
	"strings"
	"time"
)

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

	// ManagedBy is ManagedByConsole for the provider the billing console pushes; it cannot be
	// changed from Miabi.
	ManagedBy string `json:"managed_by,omitempty" gorm:"not null;default:''"`
	// Zones are the zones a console-managed provider may change; new domains under them use it.
	Zones []string `json:"zones,omitempty" gorm:"serializer:json"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ConsoleManaged reports whether the billing console owns the provider.
func (p *DNSProvider) ConsoleManaged() bool { return p.ManagedBy == ManagedByConsole }

// CoversDomain reports whether name equals one of the provider's zones or falls under one.
func (p *DNSProvider) CoversDomain(name string) bool {
	for _, z := range p.Zones {
		if name == z || strings.HasSuffix(name, "."+z) {
			return true
		}
	}
	return false
}
