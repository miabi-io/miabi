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
	"time"
)

// RouteTLSMode selects how TLS is served for a route's hosts.
type RouteTLSMode string

const (
	RouteTLSNone   RouteTLSMode = "none"
	RouteTLSACME   RouteTLSMode = "acme"   // Goma's global ACME issuer
	RouteTLSCustom RouteTLSMode = "custom" // user-supplied certificate
)

// RouteStatus is config-sync state with Goma, not upstream health.
type RouteStatus string

const (
	// RouteStatusPending is the initial state before the route has been synced.
	RouteStatusPending RouteStatus = "pending"
	RouteStatusLive    RouteStatus = "live"
	// Disabled, or a host is not under a verified domain (rendered enabled:false).
	RouteStatusOffline RouteStatus = "offline"
	RouteStatusError   RouteStatus = "error"
)

type RouteMaintenance struct {
	Enabled    bool   `json:"enabled"`
	StatusCode int    `json:"status_code,omitempty"`
	Message    string `json:"message,omitempty"`
}

// Route's container backend is injected at render time from the app's network alias, so users never set it.
type Route struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_route_workspace_name,unique;not null"`
	// Name is the unique slug handle scoped to the workspace (Goma route name).
	Name string `json:"name" gorm:"index:idx_route_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName   string       `json:"display_name"`
	ApplicationID uint         `json:"application_id" gorm:"index;not null"`
	Path          string       `json:"path" gorm:"not null;default:/"`
	Hosts         []string     `json:"hosts,omitempty" gorm:"serializer:json"`
	Methods       []string     `json:"methods,omitempty" gorm:"serializer:json"`
	Middlewares   []string     `json:"middlewares,omitempty" gorm:"serializer:json"` // middleware names
	Rewrite       string       `json:"rewrite,omitempty"`                            // path rewrite (Goma route rewrite)
	TargetPort    int          `json:"target_port"`                                  // 0 = use the app's port
	TLSMode       RouteTLSMode `json:"tls_mode" gorm:"not null;default:acme"`
	// Empty means the gateway default provider.
	TLSProvider string `json:"tls_provider,omitempty"`
	Generated   bool   `json:"generated" gorm:"not null;default:false"`
	// Supersedes structured fields, but Miabi still injects name, backends and tls so the route cannot misroute.
	AdvancedConfig string `json:"advanced_config,omitempty" gorm:"type:text"`
	// CertificateID references a stored Certificate when TLSMode=custom.
	CertificateID *uint `json:"certificate_id,omitempty" gorm:"index"`
	// ON DELETE SET NULL so the route falls back to its default TLS mode instead of dangling.
	Certificate       *Certificate     `json:"-" gorm:"foreignKey:CertificateID;constraint:OnDelete:SET NULL"`
	Enabled           bool             `json:"enabled" gorm:"not null;default:true"`
	ExploitProtection bool             `json:"exploit_protection" gorm:"not null;default:false"`
	Maintenance       RouteMaintenance `json:"maintenance" gorm:"serializer:json"`
	Status            RouteStatus      `json:"status" gorm:"not null;default:pending"`
	StatusReason      string           `json:"status_reason,omitempty"`
	// SyncedAt is when the route was last reconciled into the gateway config.
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata  Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// HasCustomCert is a transient flag for responses (never persisted).
	HasCustomCert bool `json:"has_custom_cert" gorm:"-"`

	// DNSTarget is the IPv4 address, or IPv6 when there is none; transient, populated on read.
	DNSTarget     string          `json:"dns_target,omitempty" gorm:"-"`
	DNSTargetIPv6 string          `json:"dns_target_ipv6,omitempty" gorm:"-"`
	DNSHostname   string          `json:"dns_hostname,omitempty" gorm:"-"`
	DNSRecords    []AddressRecord `json:"dns_records,omitempty" gorm:"-"`

	Backends []string `json:"backends,omitempty" gorm:"-"`
}
