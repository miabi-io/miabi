// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// RouteStatus is a route's config-sync status with the gateway — whether Goma is
// actually serving the route's config. It is NOT upstream health: RouteLive means
// "Goma is configured to serve this route", not "the backend answers 200".
type RouteStatus string

const (
	// RouteStatusPending is the initial state before the route has been synced.
	RouteStatusPending RouteStatus = "pending"
	// RouteStatusLive means the route was synced and Goma is serving it (enabled,
	// and every host falls under a verified domain).
	RouteStatusLive RouteStatus = "live"
	// RouteStatusOffline means the route is synced but not served — it is disabled,
	// or one of its hosts is not under a verified domain (rendered enabled:false).
	RouteStatusOffline RouteStatus = "offline"
	// RouteStatusError means the last attempt to push the route's config to the
	// gateway failed; StatusReason carries the detail.
	RouteStatusError RouteStatus = "error"
)

type RouteMaintenance struct {
	Enabled    bool   `json:"enabled"`
	StatusCode int    `json:"status_code,omitempty"`
	Message    string `json:"message,omitempty"`
}

// Route is a Goma Gateway route owned by a workspace and bound to an
// application. The container backend is injected by the platform at render
// time (the app's network alias), so users never set it.
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
	// TLSProvider names the Goma certManager provider that issues/serves this
	// route's cert (the multi-provider certManager). Empty = the gateway default.
	TLSProvider string `json:"tls_provider,omitempty"`
	// Generated marks a platform-generated external-access route (managed from the
	// app's External access card, shown read-only in the Routes UI).
	Generated bool `json:"generated" gorm:"not null;default:false"`
	// AdvancedConfig is a raw Goma route YAML the admin authors directly; when
	// set, it supersedes the structured fields at render time (Miabi still
	// injects name + backends + tls so the route can't misroute). Empty = simple.
	AdvancedConfig string `json:"advanced_config,omitempty" gorm:"type:text"`
	// CertificateID references a stored Certificate when TLSMode=custom.
	CertificateID *uint `json:"certificate_id,omitempty" gorm:"index"`
	// Certificate declares the FK so deleting the referenced certificate nulls
	// this pointer (ON DELETE SET NULL) instead of leaving it dangling. The route
	// falls back to its default TLS mode. Association is not serialized.
	Certificate       *Certificate     `json:"-" gorm:"foreignKey:CertificateID;constraint:OnDelete:SET NULL"`
	Enabled           bool             `json:"enabled" gorm:"not null;default:true"`
	ExploitProtection bool             `json:"exploit_protection" gorm:"not null;default:false"`
	Maintenance       RouteMaintenance `json:"maintenance" gorm:"serializer:json"`
	// Status is the route's config-sync status, set whenever the workspace proxy
	// config is reconciled.
	Status RouteStatus `json:"status" gorm:"not null;default:pending"`
	// StatusReason explains a non-live status (e.g. "domain not verified",
	// "disabled", or the gateway write error). Empty when live.
	StatusReason string `json:"status_reason,omitempty"`
	// SyncedAt is when the route was last reconciled into the gateway config.
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	// Metadata holds free-form labels; "miabi.io/" keys are platform-managed.
	Metadata  Metadata  `json:"metadata,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// HasCustomCert is a transient flag for responses (never persisted).
	HasCustomCert bool `json:"has_custom_cert" gorm:"-"`

	// DNSTarget / DNSHostname are the public address an A/AAAA (or CNAME) record
	// for this route's hosts should point to — the gateway that terminates the
	// route. Transient, populated on read.
	DNSTarget   string `json:"dns_target,omitempty" gorm:"-"`
	DNSHostname string `json:"dns_hostname,omitempty" gorm:"-"`

	// Backends are the actual upstream endpoints the gateway uses for this route
	// (the node-local DNS alias, or a port-forward node's address:hostPort).
	// Transient, populated on read so the UI shows the real backend.
	Backends []string `json:"backends,omitempty" gorm:"-"`
}
