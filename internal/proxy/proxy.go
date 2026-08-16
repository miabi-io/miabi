// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package proxy abstracts the reverse proxy / gateway (Goma Gateway). Routes and middlewares are
// written as YAML files in Goma's watched provider directory; an in-memory implementation backs
// dev and tests. TLS termination, ACME issuance and renewal are owned by Goma.
package proxy

import "context"

// CertPair is a user-supplied (custom) certificate, inlined as PEM.
type CertPair struct {
	CertPEM string
	KeyPEM  string
}

// Backend is a single upstream for a route. Weight enables canary / weighted
// load-balancing across backends; 0 means unweighted (single backend).
type Backend struct {
	Endpoint string // e.g. http://mb-app-1:8080
	Weight   int    // relative weight; probability = weight / sum(weights)
}

// RenderedRoute is the desired Goma route. Backends (the upstreams) are injected
// by the caller from the application's network alias(es). A single backend means
// all traffic goes there; multiple weighted backends enable canary splits.
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
	// TLSProvider names the Goma certManager provider that serves this route's
	// cert (multi-provider certManager). Empty = the gateway's default provider.
	TLSProvider string
	// TLSNone marks a route that opts out of TLS entirely; it renders an explicit
	// tls.provider: "none" so the gateway never tries to obtain a cert for it.
	TLSNone bool
	// Disabled renders the route with Goma's `enabled: false` so the gateway keeps it defined but
	// stops serving it. Zero value is enabled, matching Goma's default. Preferred over dropping the
	// route, which doesn't reliably take effect across the file and HTTP providers.
	Disabled bool
	// AdvancedYAML, when set, is a raw Goma route config that supersedes the
	// structured fields; Miabi still forces name/backends/tls into it.
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

// RegistryProxy is the desired gateway config for the built-in Docker registry: one route fronted
// by the HTTPS-redirect, forwardAuth and namespace-rewrite middlewares. Rendered to a dedicated
// file, separate from the per-workspace configs.
type RegistryProxy struct {
	Enabled     bool   // false removes the registry config
	Host        string // public host, e.g. registry.<domain>
	Upstream    string // http://mb-registry:5000
	AuthURL     string // forwardAuth target, e.g. http://miabi:9000/internal/registry/auth
	TLSProvider string // certManager provider ("" = gateway default)
}

// Manager applies desired proxy state. A workspace's routes and middlewares are written together
// as one unit (Goma resolves a route's middleware references within the same file), so the
// workspace is the unit of sync rather than individual resources. Implementations are idempotent.
type Manager interface {
	// SyncWorkspace replaces a workspace's entire proxy config — all its routes and
	// middlewares — atomically. Passing no routes and no middlewares removes the
	// workspace's config entirely.
	SyncWorkspace(ctx context.Context, workspaceID uint, routes []RenderedRoute, mws []RenderedMiddleware) error
	// SyncRegistry writes (or, when disabled, removes) the built-in registry's
	// gateway route + middlewares.
	SyncRegistry(ctx context.Context, cfg RegistryProxy) error
}
