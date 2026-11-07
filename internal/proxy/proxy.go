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

// Package proxy abstracts Goma Gateway; TLS termination, ACME issuance and renewal are owned by Goma.
package proxy

import "context"

// CertPair is a user-supplied (custom) certificate, inlined as PEM.
type CertPair struct {
	CertPEM string
	KeyPEM  string
}

// Source, Operator and their valid values mirror the gateway's.
type MatchRule struct {
	Source   string // header | query | cookie | ip
	Name     string // header/query/cookie key; unused for ip
	Operator string // equals | not_equals | contains | not_contains | starts_with | ends_with | regex | in
	Value    string // compared value; comma-separated list for `in`
}

// A matching Exclusive backend takes all matching traffic; a non-exclusive one joins the weighted pool.
type Backend struct {
	Endpoint  string // e.g. http://mb-app-1:8080
	Weight    int    // relative weight; probability = weight / sum(weights)
	Exclusive bool
	Priority  int
	Match     []MatchRule
}

type RenderedRoute struct {
	ID          uint
	WorkspaceID uint // namespaces the Goma name (mb-ws<id>-<name>) to avoid cross-workspace collisions
	Name        string
	Path        string
	Hosts       []string
	Methods     []string
	Rewrite     string    // path rewrite
	Middlewares []string  // referenced middleware names
	Backends    []Backend // one or more weighted upstreams
	Certs       []CertPair
	// Empty means the gateway's default provider.
	TLSProvider string
	// Renders tls.provider "none" so the gateway never tries to obtain a cert.
	TLSNone bool
	// Preferred over dropping the route, which does not reliably take effect across the file and HTTP providers.
	Disabled bool
	// Miabi still forces name, backends and tls into it.
	AdvancedYAML      string
	ExploitProtection bool
	Maintenance       *RouteMaintenance
}

type RouteMaintenance struct {
	Enabled    bool
	StatusCode int
	Message    string
}

// RenderedMiddleware is the desired Goma middleware definition.
type RenderedMiddleware struct {
	ID          uint
	WorkspaceID uint // namespaces the Goma name (mb-ws<id>-<name>); routes reference it the same way
	Name        string
	Type        string
	Paths       []string
	Rule        map[string]interface{}
}

type RegistryProxy struct {
	Enabled     bool   // false removes the registry config
	Host        string // public host, e.g. registry.<domain>
	Upstream    string // http://mb-registry:5000
	AuthURL     string // forwardAuth target, e.g. http://miabi:9000/internal/registry/auth
	TLSProvider string // certManager provider ("" = gateway default)
	// Replaces the tenant's header upstream, after forwardAuth has already decided; empty renders no middleware.
	UpstreamAuth string
	// Off behind a TLS terminator without trusted proxies, where Goma would redirect forever. See RegistryConfig.
	HTTPSRedirect bool
}

// The workspace is the unit of sync because Goma resolves middleware references within one file.
type Manager interface {
	// Passing no routes and no middlewares removes the workspace's config entirely.
	SyncWorkspace(ctx context.Context, workspaceID uint, routes []RenderedRoute, mws []RenderedMiddleware) error
	SyncRegistry(ctx context.Context, cfg RegistryProxy) error
}
