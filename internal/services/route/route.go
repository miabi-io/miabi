// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package route manages Goma Gateway routes bound to applications and
// reconciles them into the proxy provider. The container backend is injected
// from the application's network alias at render time.
package route

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/mwcatalog"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gopkg.in/yaml.v3"
)

var (
	ErrNameRequired = errors.New("route name is required")
	ErrInvalidName  = errors.New("name must be lowercase letters, digits and hyphens (e.g. my-api)")
	// ErrHostRequired rejects a structured route with no hostname: a hostless route
	// on path "/" matches every request, so the gateway would funnel all traffic to
	// it. (Advanced-config routes declare their hosts in the raw YAML.)
	ErrHostRequired = errors.New("at least one host is required")
	ErrNameTaken    = errors.New("a route with this name already exists")
	ErrAppRequired  = errors.New("application not found in workspace")
	ErrNotFound     = errors.New("route not found")
	ErrCertRequired = errors.New("custom TLS requires a stored certificate")
	ErrInvalidYAML  = errors.New("advanced config is not valid YAML")
	// ErrDomainNotRegistered is returned when a route host does not fall under
	// any domain registered in the workspace. Register the domain first.
	ErrDomainNotRegistered = errors.New("no matching domain is registered in this workspace; add the domain first")
	// ErrDomainBanned is returned when a route host falls under a domain a platform
	// admin has banned. Banned domains can never be served.
	ErrDomainBanned = errors.New("this domain has been banned by a platform administrator")
	// ErrHostTaken is returned when a route's hostname is already claimed by
	// another route. Hostnames are globally unique (a host maps to exactly one
	// route): another workspace owning the host is rejected outright, and within a
	// workspace the same host may only be reused on a different path.
	ErrHostTaken = errors.New("hostname is already used by another route")
	// ErrNodeAddressRequired is returned when routing an app on a port-forward
	// node that has no address the gateway can reach it at.
	ErrNodeAddressRequired = errors.New("the node has no address set; set its private address before adding a route")
	// ErrAdvancedTLSCert rejects an advanced route config that carries an inline TLS
	// certificate/key. Miabi manages TLS (route TLS mode + a domain-validated stored
	// certificate), so a hand-typed cert — which could assert a host the workspace
	// doesn't control — is refused rather than silently dropped.
	ErrAdvancedTLSCert = errors.New("set TLS via the route's TLS mode and a managed certificate, not an inline tls certificate in advanced config")
	// ErrMiddlewareRequired is returned when an attach/detach call omits the name.
	ErrMiddlewareRequired = errors.New("middleware name is required")
	// ErrMiddlewareNotFound is returned when attaching a middleware that does not
	// exist in the workspace (detach is idempotent and never returns this).
	ErrMiddlewareNotFound = errors.New("middleware not found in this workspace")
)

// validateAdvanced ensures a non-empty advanced config parses as YAML and does
// not try to set an inline TLS certificate (Miabi owns TLS).
func validateAdvanced(cfg string) error {
	if strings.TrimSpace(cfg) == "" {
		return nil
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(cfg), &out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidYAML, err)
	}
	if advancedHasInlineCert(out) {
		return ErrAdvancedTLSCert
	}
	return nil
}

// advancedHasInlineCert reports whether an advanced route config tries to set an
// inline TLS certificate — i.e. a `tls` mapping carrying certificate/key material
// (Goma spells it `tls.certificate.{cert,key}`; we also catch the common
// `certificate`/`cert`/`key`/`certFile`/`keyFile` spellings defensively).
func advancedHasInlineCert(m map[string]any) bool {
	tls, ok := m["tls"].(map[string]any)
	if !ok {
		return false
	}
	for _, k := range []string{"certificate", "cert", "key", "certFile", "keyFile"} {
		if _, present := tls[k]; present {
			return true
		}
	}
	return false
}

// CertResolver resolves a stored certificate's PEM + decrypted key for proxy
// rendering. Implemented by the certificate service; injected after construction.
type CertResolver interface {
	Resolve(workspaceID, id uint) (certPEM, keyPEM string, err error)
}

type Service struct {
	routes      *repositories.RouteRepository
	middlewares *repositories.MiddlewareRepository
	apps        *repositories.ApplicationRepository
	releases    *repositories.ReleaseRepository
	servers     *repositories.ServerRepository
	ports       *repositories.PortBindingRepository
	proxy       proxy.Manager
	certs       CertResolver
	attacher    ProxyAttacher
	publisher   PortPublisher
	domains     DomainLister
	wsPolicy    WorkspacePolicy
	dnsAddr     DNSAddresser
	reloader    EdgeReloader
	cluster     ClusterCap
	minPort     int
	maxPort     int
}

// ClusterCap reports whether the manager engine is a reachable swarm manager.
// Implemented by services/cluster.
type ClusterCap interface {
	CapCluster() bool
}

// SetCluster wires swarm detection (nil-safe; nil = never cluster mode, so
// remote nodes keep being reached by published host port).
func (s *Service) SetCluster(c ClusterCap) { s.cluster = c }

func (s *Service) clusterOn() bool { return s.cluster != nil && s.cluster.CapCluster() }

// EdgeReloader notifies edge-gateway nodes to pull their configuration
// immediately after a change, instead of waiting for their HTTP-provider poll
// interval. Calls are best-effort. Optional: when unset, nodes still converge on
// their next poll.
type EdgeReloader interface {
	ReloadServers(ctx context.Context, serverIDs []uint)
}

// SetEdgeReloader wires the edge-gateway reloader used after a workspace proxy
// sync. Optional.
func (s *Service) SetEdgeReloader(r EdgeReloader) { s.reloader = r }

// DNSAddresser keeps an app's public A/AAAA/CNAME records in sync with its routed
// hosts (for hosts under a verified, provider-connected domain). Injected after
// construction; nil disables app-address automation. Implemented by the
// dnsprovider service.
type DNSAddresser interface {
	ReconcileAppAddresses(ctx context.Context, workspaceID, appID uint, hosts []string, ip, hostname string) error
}

// SetDNSAddresser wires app-address DNS automation (nil-safe; nil = off).
func (s *Service) SetDNSAddresser(a DNSAddresser) { s.dnsAddr = a }

// DomainLister lists the workspace's registered domains. A user-created route's
// host must fall under one of them (the domain must be registered first);
// platform-generated external-access routes bypass this, as they don't go
// through Create. Injected after construction; nil disables the check.
type DomainLister interface {
	ListByWorkspace(workspaceID uint) ([]models.Domain, error)
}

// SetDomains wires the domain registry used to gate route hostnames.
func (s *Service) SetDomains(d DomainLister) { s.domains = d }

// WorkspacePolicy reports per-workspace serving policy. A privileged workspace
// may expose routes whose domains are registered but not yet verified (a ban
// still blocks them). Implemented by the workspace repository; injected after
// construction (nil = no workspace is treated as privileged).
type WorkspacePolicy interface {
	IsPrivileged(workspaceID uint) (bool, error)
}

// SetWorkspacePolicy wires the privileged-workspace lookup (nil-safe).
func (s *Service) SetWorkspacePolicy(p WorkspacePolicy) { s.wsPolicy = p }

// privileged reports whether a workspace may bypass the domain-verification gate.
// A missing policy or a lookup error is treated as not privileged (fail closed).
func (s *Service) privileged(workspaceID uint) bool {
	if s.wsPolicy == nil {
		return false
	}
	p, err := s.wsPolicy.IsPrivileged(workspaceID)
	if err != nil {
		logger.Warn("failed to load workspace privilege for route gate", "workspace", workspaceID, "err", err)
		return false
	}
	return p
}

// validateHosts requires every non-empty host to fall under a domain registered
// in the workspace. A catch-all route (no hosts) and an unset domain registry
// are both allowed.
func (s *Service) validateHosts(workspaceID uint, hosts []string) error {
	if s.domains == nil {
		return nil
	}
	wanted := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			wanted = append(wanted, h)
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	domains, err := s.domains.ListByWorkspace(workspaceID)
	if err != nil {
		return err
	}
	for _, h := range wanted {
		d := matchDomain(h, domains)
		if d == nil {
			return fmt.Errorf("%w: %s", ErrDomainNotRegistered, h)
		}
		if d.Banned {
			return fmt.Errorf("%w: %s", ErrDomainBanned, h)
		}
	}
	return nil
}

// normalizeHosts lowercases, trims, and de-duplicates a route's hostnames so they
// store and compare canonically (DNS is case-insensitive).
func normalizeHosts(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	seen := map[string]bool{}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// normalizePath canonicalizes a route path for comparison: empty becomes "/".
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	return p
}

// ensureHostsAvailable enforces globally-unique route hostnames. A hostname maps
// to exactly one owning workspace: if any wanted host is already routed by a
// DIFFERENT workspace, the route is rejected (prevents cross-tenant host
// hijacking — domains are unique per workspace, so two workspaces can register
// the same name). Within the same workspace, a host may be reused only on a
// different path (host+path routing). excludeID skips the route being updated.
func (s *Service) ensureHostsAvailable(workspaceID uint, hosts []string, path string, excludeID uint) error {
	wanted := map[string]bool{}
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			wanted[h] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	path = normalizePath(path)
	all, err := s.routes.ListAll()
	if err != nil {
		return err
	}
	for i := range all {
		r := &all[i]
		if r.ID == excludeID {
			continue
		}
		for _, h := range r.Hosts {
			if !wanted[strings.ToLower(strings.TrimSpace(h))] {
				continue
			}
			if r.WorkspaceID != workspaceID {
				return fmt.Errorf("%w: %s is routed by another workspace", ErrHostTaken, h)
			}
			if normalizePath(r.Path) == path {
				return fmt.Errorf("%w: %s%s already exists", ErrHostTaken, h, path)
			}
		}
	}
	return nil
}

// hostUnderDomain reports whether host equals or is a subdomain of a registered
// domain (e.g. "shop.example.com" is covered by "example.com").
func hostUnderDomain(host string, domains []models.Domain) bool {
	for i := range domains {
		name := strings.ToLower(domains[i].Name)
		if host == name || strings.HasSuffix(host, "."+name) {
			return true
		}
	}
	return false
}

// matchDomain returns the most specific registered domain covering host (the
// longest matching name), or nil when none does. A wildcard domain matches by
// name like any other — ownership is proven once for the whole zone.
func matchDomain(host string, domains []models.Domain) *models.Domain {
	var best *models.Domain
	for i := range domains {
		name := strings.ToLower(domains[i].Name)
		if host == name || strings.HasSuffix(host, "."+name) {
			if best == nil || len(name) > len(best.Name) {
				best = &domains[i]
			}
		}
	}
	return best
}

// hasHost reports whether the list contains at least one non-blank hostname.
func hasHost(hosts []string) bool {
	for _, h := range hosts {
		if strings.TrimSpace(h) != "" {
			return true
		}
	}
	return false
}

// routeServeState computes a route's intended config-sync status and whether the
// gateway should serve it. A route is served only when it is enabled AND (when
// the domain gate is active) every host clears its covering domain: not banned,
// and verified — unless the workspace is privileged, which waives verification
// (but never a ban). A failing host renders the route disabled (enabled:false)
// and reported offline with a reason. gate is false when the domain registry is
// not wired, mirroring validateHosts.
//
// Platform-generated external-access routes are exempt from the domain gate: they
// serve over the platform's own base domain, so they go live as soon as they're
// enabled rather than waiting on a user-registered, verified domain.
func routeServeState(rt *models.Route, domains []models.Domain, gate, privileged bool) (serve bool, status models.RouteStatus, reason string) {
	if !rt.Enabled {
		return false, models.RouteStatusOffline, "disabled"
	}
	if rt.Generated {
		return true, models.RouteStatusLive, ""
	}
	// A structured route with no hostname matches every request on its path, so the
	// gateway would funnel all traffic to it. Refuse to serve it (advanced-config
	// routes carry their hosts in the raw YAML, so they are exempt here).
	if strings.TrimSpace(rt.AdvancedConfig) == "" && !hasHost(rt.Hosts) {
		return false, models.RouteStatusOffline, "route has no hosts"
	}
	if gate {
		for _, h := range rt.Hosts {
			h = strings.ToLower(strings.TrimSpace(h))
			if h == "" {
				continue
			}
			d := matchDomain(h, domains)
			switch {
			case d == nil:
				return false, models.RouteStatusOffline, "domain not registered: " + h
			case d.Banned:
				return false, models.RouteStatusOffline, "domain banned: " + h
			case !d.Verified && !privileged:
				return false, models.RouteStatusOffline, "domain not verified: " + h
			}
		}
	}
	return true, models.RouteStatusLive, ""
}

// workspaceDomains loads a workspace's registered domains for the verified-host
// gate. A nil registry (or error) yields nil, which disables gating — matching
// validateHosts, where an unset registry means "don't gate".
func (s *Service) workspaceDomains(workspaceID uint) []models.Domain {
	if s.domains == nil {
		return nil
	}
	domains, err := s.domains.ListByWorkspace(workspaceID)
	if err != nil {
		logger.Warn("failed to load domains for route gate", "workspace", workspaceID, "err", err)
		return nil
	}
	return domains
}

// persistRouteStatus records a route's config-sync status, skipping the write
// when nothing changed (so node pulls and re-syncs don't churn the row).
func (s *Service) persistRouteStatus(rt *models.Route, status models.RouteStatus, reason string, syncedAt time.Time) {
	if rt.Status == status && rt.StatusReason == reason {
		return
	}
	if err := s.routes.UpdateStatus(rt.ID, status, reason, syncedAt); err != nil {
		logger.Warn("failed to persist route status", "route", rt.ID, "err", err)
	}
}

// PortPublisher ensures a port-forward app's container actually publishes the
// host ports its routes need, redeploying (rolling) if the live container is
// missing them. Implemented by the application service; injected after
// construction to avoid an import cycle. nil = no auto-redeploy (the host port
// then publishes on the next natural deploy).
type PortPublisher interface {
	EnsurePublished(ctx context.Context, appID uint) error
}

// SetPortPublisher wires the publisher used to (re)publish host ports when a
// port-forward app gains a route.
func (s *Service) SetPortPublisher(p PortPublisher) { s.publisher = p }

// ProxyAttacher reconciles whether an application's running container(s) are
// attached to the shared reverse-proxy network, so only route-exposed apps join
// it. Implemented by the deploy worker (which holds the per-node Docker clients);
// optional (nil = attachment is decided only at deploy time).
type ProxyAttacher interface {
	ReconcileProxyAttachment(ctx context.Context, appID uint, attached bool) error
}

// SetCertResolver wires the certificate store used to resolve custom-cert routes.
func (s *Service) SetCertResolver(r CertResolver) { s.certs = r }

// SetProxyAttacher wires the proxy-network reconciler used when routes change.
func (s *Service) SetProxyAttacher(a ProxyAttacher) { s.attacher = a }

// RoutesUsingCertificate returns the workspace's routes that reference a stored
// certificate (for the certificate store's delete guard / usage list).
func (s *Service) RoutesUsingCertificate(workspaceID, certID uint) ([]models.Route, error) {
	return s.routes.ListByCertificate(workspaceID, certID)
}

func NewService(
	routes *repositories.RouteRepository,
	middlewares *repositories.MiddlewareRepository,
	apps *repositories.ApplicationRepository,
	releases *repositories.ReleaseRepository,
	servers *repositories.ServerRepository,
	ports *repositories.PortBindingRepository,
	proxyMgr proxy.Manager,
	minPort, maxPort int,
) *Service {
	return &Service{routes: routes, middlewares: middlewares, apps: apps, releases: releases, servers: servers, ports: ports, proxy: proxyMgr, minPort: minPort, maxPort: maxPort}
}

type Input struct {
	// Name is the unique slug handle; it must already be canonical slug form (it
	// becomes the Goma route name). DisplayName is the free-text label (falls back
	// to Name when blank).
	Name           string
	DisplayName    string
	ApplicationID  uint
	Path           string
	Hosts          []string
	Methods        []string
	Middlewares    []string
	Rewrite        string
	TargetPort     int
	TLSMode        models.RouteTLSMode
	AdvancedConfig string // raw Goma route YAML; supersedes structured fields
	CertificateID  *uint  // stored certificate (required for custom TLS)
	Enabled        *bool
	// Metadata carries provenance labels (managed-by, gitops-source) for routes
	// created by the apply/GitOps engine, so a route participates in prune and
	// per-project teardown like every other kind. Nil for UI-created routes and on
	// a UI edit, which leaves any existing labels untouched.
	Metadata models.Metadata
}

func (s *Service) Create(ctx context.Context, workspaceID uint, in Input) (*models.Route, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if !slug.IsValid(name) {
		return nil, ErrInvalidName
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = name
	}
	app, err := s.apps.FindInWorkspace(workspaceID, in.ApplicationID)
	if err != nil {
		return nil, ErrAppRequired
	}
	if err := s.requireRoutableNode(app); err != nil {
		return nil, err
	}
	in.Hosts = normalizeHosts(in.Hosts)
	if strings.TrimSpace(in.AdvancedConfig) == "" && len(in.Hosts) == 0 {
		return nil, ErrHostRequired
	}
	if err := s.validateHosts(workspaceID, in.Hosts); err != nil {
		return nil, err
	}
	if err := s.ensureHostsAvailable(workspaceID, in.Hosts, in.Path, 0); err != nil {
		return nil, err
	}
	taken, err := s.routes.ExistsByName(workspaceID, name)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrNameTaken
	}
	if err := validateAdvanced(in.AdvancedConfig); err != nil {
		return nil, err
	}
	rt := &models.Route{
		WorkspaceID:    workspaceID,
		ApplicationID:  app.ID,
		Name:           name,
		DisplayName:    displayName,
		Path:           defaultPath(in.Path),
		Hosts:          in.Hosts,
		Methods:        in.Methods,
		Middlewares:    in.Middlewares,
		Rewrite:        in.Rewrite,
		TargetPort:     in.TargetPort,
		TLSMode:        defaultTLS(in.TLSMode),
		AdvancedConfig: strings.TrimSpace(in.AdvancedConfig),
		CertificateID:  in.CertificateID,
		Enabled:        in.Enabled == nil || *in.Enabled,
		Metadata:       in.Metadata,
	}
	if err := s.applyCert(rt, in); err != nil {
		return nil, err
	}
	if err := s.routes.Create(rt); err != nil {
		return nil, err
	}
	_ = s.SyncRoute(ctx, app.ID)
	return strip(rt), nil
}

func (s *Service) Update(ctx context.Context, workspaceID, id uint, in Input) (*models.Route, error) {
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	// Re-link the target application when the caller supplies one. apply/GitOps
	// passes the resolved app on every reconcile, so a route whose link is stale
	// (e.g. the app was recreated with a new id) or empty converges instead of
	// drifting forever. The UI edit form leaves ApplicationID 0 to keep the
	// current link.
	if in.ApplicationID != 0 && in.ApplicationID != rt.ApplicationID {
		app, aerr := s.apps.FindInWorkspace(workspaceID, in.ApplicationID)
		if aerr != nil {
			return nil, ErrAppRequired
		}
		rt.ApplicationID = app.ID
	}
	if app, aerr := s.apps.FindByID(rt.ApplicationID); aerr == nil {
		if err := s.requireRoutableNode(app); err != nil {
			return nil, err
		}
	}
	if in.Name != "" {
		name := strings.TrimSpace(in.Name)
		if !slug.IsValid(name) {
			return nil, ErrInvalidName
		}
		rt.Name = name
	}
	if in.Path != "" {
		rt.Path = in.Path
	}
	// Generated external-access routes manage their own hosts; only user routes
	// are gated on a registered domain and global hostname uniqueness.
	if !rt.Generated {
		in.Hosts = normalizeHosts(in.Hosts)
		if strings.TrimSpace(in.AdvancedConfig) == "" && len(in.Hosts) == 0 {
			return nil, ErrHostRequired
		}
		if err := s.validateHosts(workspaceID, in.Hosts); err != nil {
			return nil, err
		}
		if err := s.ensureHostsAvailable(workspaceID, in.Hosts, rt.Path, rt.ID); err != nil {
			return nil, err
		}
	}
	rt.Hosts = in.Hosts
	rt.Methods = in.Methods
	rt.Middlewares = in.Middlewares
	rt.Rewrite = in.Rewrite
	rt.TargetPort = in.TargetPort
	if in.TLSMode != "" {
		rt.TLSMode = in.TLSMode
	}
	if err := validateAdvanced(in.AdvancedConfig); err != nil {
		return nil, err
	}
	rt.AdvancedConfig = strings.TrimSpace(in.AdvancedConfig)
	rt.CertificateID = in.CertificateID
	if in.Enabled != nil {
		rt.Enabled = *in.Enabled
	}
	// Re-stamp provenance labels when apply/GitOps supplies them; a UI edit passes
	// nil so existing labels are preserved.
	if in.Metadata != nil {
		rt.Metadata = in.Metadata
	}
	if err := s.applyCert(rt, in); err != nil {
		return nil, err
	}
	if err := s.routes.Update(rt); err != nil {
		return nil, err
	}
	_ = s.SyncRoute(ctx, rt.ApplicationID)
	return strip(rt), nil
}

// SetEnabled flips a route's enabled flag without touching any other field
// (a partial update), then reconciles the proxy.
func (s *Service) SetEnabled(ctx context.Context, workspaceID, id uint, enabled bool) (*models.Route, error) {
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	rt.Enabled = enabled
	if err := s.routes.Update(rt); err != nil {
		return nil, err
	}
	_ = s.SyncRoute(ctx, rt.ApplicationID)
	strip(rt)
	s.enrichDNS(rt)
	return rt, nil
}

// AttachMiddleware adds a workspace middleware to a route's chain without
// touching any other field (a partial update), then reconciles the proxy. It is
// idempotent — re-attaching an already-present middleware is a no-op — and the
// named middleware must exist in the workspace. Unlike a full Update this works
// on platform-generated external-access routes too, so operators can layer
// auth / rate-limit / header middlewares onto managed routes without editing them.
func (s *Service) AttachMiddleware(ctx context.Context, workspaceID, id uint, name string) (*models.Route, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrMiddlewareRequired
	}
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	exists, err := s.middlewares.ExistsByName(workspaceID, name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrMiddlewareNotFound
	}
	if !slices.Contains(rt.Middlewares, name) {
		rt.Middlewares = append(rt.Middlewares, name)
		if err := s.routes.Update(rt); err != nil {
			return nil, err
		}
		_ = s.SyncRoute(ctx, rt.ApplicationID)
	}
	strip(rt)
	s.enrichDNS(rt)
	return rt, nil
}

// DetachMiddleware removes a middleware from a route's chain (a partial update),
// then reconciles the proxy. Idempotent: detaching a middleware the route does
// not carry is a no-op. Works on generated routes too.
func (s *Service) DetachMiddleware(ctx context.Context, workspaceID, id uint, name string) (*models.Route, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrMiddlewareRequired
	}
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	before := len(rt.Middlewares)
	rt.Middlewares = slices.DeleteFunc(rt.Middlewares, func(m string) bool { return m == name })
	if len(rt.Middlewares) != before {
		if err := s.routes.Update(rt); err != nil {
			return nil, err
		}
		_ = s.SyncRoute(ctx, rt.ApplicationID)
	}
	strip(rt)
	s.enrichDNS(rt)
	return rt, nil
}

func (s *Service) Get(workspaceID, id uint) (*models.Route, error) {
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	strip(rt)
	s.enrichDNS(rt)
	return rt, nil
}

func (s *Service) List(workspaceID uint) ([]models.Route, error) {
	routes, err := s.routes.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range routes {
		strip(&routes[i])
		s.enrichDNS(&routes[i])
	}
	return routes, nil
}

func (s *Service) ListByApp(workspaceID, appID uint) ([]models.Route, error) {
	routes, err := s.routes.ListByApp(appID)
	if err != nil {
		return nil, err
	}
	out := routes[:0]
	for i := range routes {
		if routes[i].WorkspaceID == workspaceID {
			strip(&routes[i])
			s.enrichDNS(&routes[i])
			out = append(out, routes[i])
		}
	}
	return out, nil
}

func (s *Service) Delete(ctx context.Context, workspaceID, id uint) error {
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	if err := s.routes.Delete(rt.ID); err != nil {
		return err
	}
	// Re-sync the app's remaining routes (re-renders the workspace file without the
	// deleted route) and reconcile its proxy-network membership (detaches the app
	// when this was its last route).
	_ = s.SyncRoute(ctx, rt.ApplicationID)
	return nil
}

// partitionGeneratedOrphans splits an app's routes into the ones to keep and the
// generated external-access routes whose target port the app no longer exposes
// (orphans to tear down). A generated route is valid only while its port is the
// app's primary Port or one of its declared Ports; user-created routes are always
// kept, as they may legitimately target any port.
func partitionGeneratedOrphans(app *models.Application, routes []models.Route) (keep, orphans []models.Route) {
	valid := map[int]bool{}
	if app.Port > 0 {
		valid[app.Port] = true
	}
	for i := range app.Ports {
		valid[app.Ports[i].ContainerPort] = true
	}
	for i := range routes {
		if routes[i].Generated && !valid[routes[i].TargetPort] {
			orphans = append(orphans, routes[i])
			continue
		}
		keep = append(keep, routes[i])
	}
	return keep, orphans
}

// RemoveAppRoutes deletes every route of an application — generated
// external-access routes and user-created ones alike — and removes each from the
// proxy. Called when the application itself is deleted, so no route is left
// dangling against a now-removed app (in the database or the gateway).
func (s *Service) RemoveAppRoutes(ctx context.Context, appID uint) error {
	routes, err := s.routes.ListByApp(appID)
	if err != nil {
		return err
	}
	// The app has no routes now, so detach it from the shared proxy network.
	if s.attacher != nil {
		_ = s.attacher.ReconcileProxyAttachment(ctx, appID, false)
	}
	// Remove any managed public DNS records for this app (best-effort).
	if s.dnsAddr != nil {
		wsID := uint(0)
		if len(routes) > 0 {
			wsID = routes[0].WorkspaceID
		} else if app, aerr := s.apps.FindByID(appID); aerr == nil {
			wsID = app.WorkspaceID
		}
		if wsID != 0 {
			_ = s.dnsAddr.ReconcileAppAddresses(ctx, wsID, appID, nil, "", "")
		}
	}
	if len(routes) == 0 {
		return nil
	}
	workspaceID := routes[0].WorkspaceID
	for i := range routes {
		if err := s.routes.Delete(routes[i].ID); err != nil {
			return err
		}
	}
	// Re-render the workspace file without this app's (now deleted) routes.
	return s.SyncWorkspaceProxy(ctx, workspaceID)
}

// SyncRoute reconciles all of an application's routes into the proxy. Routes are
// removed while the app has no active release; otherwise enabled routes are
// upserted with the backend pointing at the app's network alias. Generated
// external-access routes whose target port the app no longer exposes are torn
// down first, so a generated route never outlives the port it fronts.
func (s *Service) SyncRoute(ctx context.Context, appID uint) error {
	app, err := s.apps.FindByID(appID)
	if err != nil {
		return err
	}
	routes, err := s.routes.ListByApp(appID)
	if err != nil {
		return err
	}
	// Tear down generated external-access routes whose target port the app no
	// longer exposes (the port was removed, or external access disabled for it),
	// so a generated route never outlives the port it fronts. User-created routes
	// are left untouched — they may legitimately target any port.
	kept, orphans := partitionGeneratedOrphans(app, routes)
	for i := range orphans {
		_ = s.routes.Delete(orphans[i].ID)
	}
	routes = kept
	// Keep the app on the shared proxy network only while it has a route; a
	// running container is attached/detached live so route changes take effect
	// without a redeploy.
	if s.attacher != nil {
		_ = s.attacher.ReconcileProxyAttachment(ctx, appID, len(routes) > 0)
	}
	// Keep the app's public A/AAAA/CNAME records in sync with its routed hosts (a
	// no-op unless a host falls under a verified, provider-connected domain). Runs
	// for every app type — the DNS target follows the serving gateway (dnsTarget).
	s.reconcileAppDNS(ctx, app, routes)
	// Cluster (service) apps are always served centrally via their overlay service
	// VIP — they don't use edge gateways or port-forward host ports. The attacher
	// joins the central gateway to the workspace overlay so it can resolve the VIP.
	if app.RuntimeKind == models.RuntimeService {
		return s.SyncWorkspaceProxy(ctx, app.WorkspaceID)
	}
	// edge-gateway nodes serve their own routes via the HTTP-provider endpoint, so
	// the central proxy must not carry them; SyncWorkspaceProxy already excludes
	// edge apps, so a workspace re-render drops them from the central file.
	if s.edgeGateway(app) {
		return s.SyncWorkspaceProxy(ctx, app.WorkspaceID)
	}
	// Pre-render side effects for a centrally-served app: on a port-forward node,
	// ensure each routed port has its ingress host port allocated (backendsFor
	// provisions it), which SyncWorkspaceProxy then reads back read-only. Track the
	// enabled ports so managed bindings for ports no longer routed get released.
	enabledPorts := map[int]bool{}
	if app.CurrentReleaseID != nil {
		for i := range routes {
			rt := &routes[i]
			port := routePort(rt, app)
			if rt.Enabled {
				enabledPorts[port] = true
			}
			_ = s.backendsFor(app, port)
		}
	}
	if isRemotePortForward(s.serverFor(app)) {
		s.reconcileManagedPorts(app.ID, enabledPorts)
		if s.publisher != nil {
			_ = s.publisher.EnsurePublished(ctx, app.ID)
		}
	}
	return s.SyncWorkspaceProxy(ctx, app.WorkspaceID)
}

// renderMiddlewares decrypts each middleware's secret rule fields (e.g. basicAuth
// passwords) and builds the proxy view. Fail-closed: a middleware whose secrets
// can't be decrypted is skipped rather than rendering ciphertext as a credential.
func renderMiddlewares(mws []models.Middleware) []proxy.RenderedMiddleware {
	out := make([]proxy.RenderedMiddleware, 0, len(mws))
	for i := range mws {
		m := &mws[i]
		rule, err := mwcatalog.DecryptSecrets(m.Type, m.Rule)
		if err != nil {
			logger.Error("middleware: decrypt secret rule failed; skipping", "workspace", m.WorkspaceID, "middleware", m.Name, "error", err)
			continue
		}
		out = append(out, proxy.RenderedMiddleware{ID: m.ID, WorkspaceID: m.WorkspaceID, Name: m.Name, Type: m.Type, Paths: m.Paths, Rule: rule})
	}
	return out
}

// SyncWorkspaceProxy re-renders a workspace's entire central Goma config — every
// middleware plus the routes of its centrally-served apps — as one file. This is
// the single write path for the central proxy, so a route and the middlewares it
// references are always published together. Apps with no active release, and apps
// on edge-gateway nodes (which serve their own routes over the HTTP provider), are
// excluded.
func (s *Service) SyncWorkspaceProxy(ctx context.Context, workspaceID uint) error {
	mws, err := s.middlewares.ListByWorkspace(workspaceID)
	if err != nil {
		return err
	}
	renderedMw := renderMiddlewares(mws)

	apps, err := s.apps.ListByWorkspace(workspaceID)
	if err != nil {
		return err
	}
	// A route is only served when all its hosts fall under a verified (or, for a
	// privileged workspace, merely registered) and un-banned domain; otherwise it
	// is rendered disabled and reported offline. gate is off when the registry
	// isn't wired (mirrors validateHosts).
	gate := s.domains != nil
	domains := s.workspaceDomains(workspaceID)
	privileged := s.privileged(workspaceID)
	var renderedRoutes []proxy.RenderedRoute
	// pending tracks the status each route should get once the gateway write
	// outcome is known (committed below).
	var pending []routeStatus
	// edgeServers collects the edge-gateway nodes whose own (HTTP-provider) config
	// this sync may have changed, so they can be told to pull immediately.
	edgeServers := map[uint]struct{}{}
	for i := range apps {
		// ListByWorkspace doesn't preload Ports (needed for backend scheme), so
		// load the app fully.
		app, err := s.apps.FindByID(apps[i].ID)
		if err != nil {
			continue
		}
		if app.CurrentReleaseID == nil {
			continue
		}
		// edge-gateway nodes serve their own (container) routes over the HTTP
		// provider; exclude them centrally. Cluster service apps are the exception —
		// they are always served centrally via their overlay VIP.
		if app.RuntimeKind != models.RuntimeService && s.edgeGateway(app) {
			edgeServers[app.ServerID] = struct{}{}
			continue
		}
		routes, err := s.routes.ListByApp(app.ID)
		if err != nil {
			return err
		}
		for j := range routes {
			rt := &routes[j]
			serve, status, reason := routeServeState(rt, domains, gate, privileged)
			port := routePort(rt, app)
			rr := proxy.RenderedRoute{
				ID:           rt.ID,
				WorkspaceID:  rt.WorkspaceID,
				Name:         rt.Name,
				Path:         rt.Path,
				Hosts:        rt.Hosts,
				Methods:      rt.Methods,
				Rewrite:      rt.Rewrite,
				Middlewares:  rt.Middlewares,
				Backends:     s.renderBackends(app, port),
				TLSProvider:  rt.TLSProvider,
				TLSNone:      rt.TLSMode == models.RouteTLSNone,
				Disabled:     !serve,
				AdvancedYAML: rt.AdvancedConfig,
			}
			if pair, ok := s.certPair(rt); ok {
				rr.Certs = []proxy.CertPair{pair}
			}
			renderedRoutes = append(renderedRoutes, rr)
			pending = append(pending, routeStatus{rt: rt, status: status, reason: reason})
		}
	}
	err = s.proxy.SyncWorkspace(ctx, workspaceID, renderedRoutes, renderedMw)
	s.commitRouteStatus(pending, err)
	// Tell affected edge-gateway nodes to pull their (HTTP-provider) config now,
	// best-effort and detached so a slow/unreachable node never blocks the sync.
	if err == nil && s.reloader != nil && len(edgeServers) > 0 {
		ids := make([]uint, 0, len(edgeServers))
		for id := range edgeServers {
			ids = append(ids, id)
		}
		go s.reloader.ReloadServers(context.WithoutCancel(ctx), ids)
	}
	return err
}

// routeStatus pairs a route with the config-sync status it should record once the
// gateway write outcome is known.
type routeStatus struct {
	rt     *models.Route
	status models.RouteStatus
	reason string
}

// commitRouteStatus persists the post-sync status for a batch of routes. When the
// gateway write failed, every route is marked error with the failure reason;
// otherwise each route gets its computed live/offline status and a fresh
// synced-at timestamp.
func (s *Service) commitRouteStatus(pending []routeStatus, syncErr error) {
	now := time.Now()
	for _, p := range pending {
		if syncErr != nil {
			s.persistRouteStatus(p.rt, models.RouteStatusError, syncErr.Error(), now)
			continue
		}
		s.persistRouteStatus(p.rt, p.status, p.reason, now)
	}
}

// ResyncAllProxy re-renders every workspace that has any route or middleware into
// the proxy. Run once at startup to populate the per-workspace config files —
// e.g. after upgrading from the previous one-file-per-resource layout.
func (s *Service) ResyncAllProxy(ctx context.Context) error {
	ws := map[uint]bool{}
	routes, err := s.routes.ListAll()
	if err != nil {
		return err
	}
	for i := range routes {
		ws[routes[i].WorkspaceID] = true
	}
	mws, err := s.middlewares.ListAll()
	if err != nil {
		return err
	}
	for i := range mws {
		ws[mws[i].WorkspaceID] = true
	}
	for id := range ws {
		if err := s.SyncWorkspaceProxy(ctx, id); err != nil {
			logger.Warn("proxy resync failed", "workspace", id, "err", err)
		}
	}
	return nil
}

// renderBackends returns a route's central-proxy upstreams read-only: it never
// allocates a host port (unlike backendsFor). A port-forward node uses its
// already-provisioned ingress host port; other placements use the node-local
// alias with canary weighting.
func (s *Service) renderBackends(app *models.Application, port int) []proxy.Backend {
	// Cluster (service) apps: target the service VIP via its overlay DNS alias.
	// Swarm load-balances across the replicas; the central gateway resolves it
	// because the attacher joined it to the workspace overlay.
	if app.RuntimeKind == models.RuntimeService {
		return []proxy.Backend{{Endpoint: fmt.Sprintf("%s://%s:%d", portScheme(app, port), node.AppAlias(app), port)}}
	}
	srv, _ := s.servers.FindByID(app.ServerID)
	if isRemotePortForward(srv) {
		if hp := s.hostPort(app.ID, port); hp > 0 && strings.TrimSpace(srv.Address) != "" {
			return []proxy.Backend{{Endpoint: fmt.Sprintf("%s://%s:%d", portScheme(app, port), srv.Address, hp)}}
		}
		return nil
	}
	return aliasBackends(app, port, portScheme(app, port))
}

// serverFor loads the app's node (nil on error), for connectivity checks.
func (s *Service) serverFor(app *models.Application) *models.Server {
	srv, err := s.servers.FindByID(app.ServerID)
	if err != nil {
		return nil
	}
	return srv
}

// requireRoutableNode rejects routing an app on a port-forward node that has no
// address the gateway can reach it at (otherwise the route would be a dead
// upstream). Other placements always have a reachable upstream.
func (s *Service) requireRoutableNode(app *models.Application) error {
	srv := s.serverFor(app)
	if isRemotePortForward(srv) && strings.TrimSpace(srv.Address) == "" {
		return ErrNodeAddressRequired
	}
	return nil
}

// reconcileManagedPorts deletes the app's managed (auto-forward) bindings whose
// container port is no longer referenced by an enabled route. The published port
// on the running container is harmless until the next deploy drops it; the
// gateway already stops targeting it when the route is removed.
func (s *Service) reconcileManagedPorts(appID uint, keep map[int]bool) {
	managed, err := s.ports.ListManagedByApp(appID)
	if err != nil {
		return
	}
	for i := range managed {
		if !keep[managed[i].ContainerPort] {
			_ = s.ports.Delete(managed[i].ID)
		}
	}
}

// edgeGateway reports whether the app runs on a node that runs its own edge gateway.
func (s *Service) edgeGateway(app *models.Application) bool {
	srv, err := s.servers.FindByID(app.ServerID)
	return err == nil && !srv.IsLocal && srv.Connectivity == models.ConnectivityEdgeGateway
}

// reconcileAppDNS upserts/prunes the app's public address records (A/AAAA/CNAME)
// to match its enabled routes' hosts, pointing at the serving gateway's public
// address. Best-effort and a no-op when no DNS addresser is wired or no host
// falls under a managed domain.
func (s *Service) reconcileAppDNS(ctx context.Context, app *models.Application, routes []models.Route) {
	if s.dnsAddr == nil {
		return
	}
	hosts := make([]string, 0)
	for i := range routes {
		if !routes[i].Enabled {
			continue
		}
		hosts = append(hosts, routes[i].Hosts...)
	}
	ip, hostname := s.dnsTarget(app)
	if err := s.dnsAddr.ReconcileAppAddresses(ctx, app.WorkspaceID, app.ID, hosts, ip, hostname); err != nil {
		logger.Warn("dns app-address reconcile failed", "app", app.ID, "error", err)
	}
}

// dnsTarget resolves the public address a route's hosts should point at: the
// gateway that terminates the route. An edge-gateway node serves its own ingress
// (use that node's public address); every other app is fronted by the
// control-plane gateway (use the local node's public address).
func (s *Service) dnsTarget(app *models.Application) (ip, hostname string) {
	if srv, err := s.servers.FindByID(app.ServerID); err == nil && srv != nil &&
		!srv.IsLocal && srv.Connectivity == models.ConnectivityEdgeGateway {
		return srv.PublicIP, srv.PublicHostname
	}
	if local, err := s.servers.FindLocal(); err == nil && local != nil {
		return local.PublicIP, local.PublicHostname
	}
	return "", ""
}

// enrichDNS populates a route's transient DNS-target + backend fields for
// responses so the UI shows the real upstream.
func (s *Service) enrichDNS(rt *models.Route) {
	app, err := s.apps.FindByID(rt.ApplicationID)
	if err != nil {
		return
	}
	rt.DNSTarget, rt.DNSHostname = s.dnsTarget(app)
	rt.Backends = s.displayBackends(app, routePort(rt, app))
}

// displayBackends returns the upstream endpoints for a route, read-only (it
// never allocates a host port — unlike backendsFor). For a port-forward node it
// reflects the already-provisioned address:hostPort, or nil while none exists.
func (s *Service) displayBackends(app *models.Application, port int) []string {
	if app.RuntimeKind == models.RuntimeService {
		return []string{fmt.Sprintf("%s://%s:%d", portScheme(app, port), node.AppAlias(app), port)}
	}
	if isRemotePortForward(s.serverFor(app)) {
		srv := s.serverFor(app)
		if hp := s.hostPort(app.ID, port); hp > 0 && strings.TrimSpace(srv.Address) != "" {
			return []string{fmt.Sprintf("%s://%s:%d", portScheme(app, port), srv.Address, hp)}
		}
		return nil // not yet provisioned (e.g. app not deployed)
	}
	out := make([]string, 0, 2)
	for _, b := range aliasBackends(app, port, portScheme(app, port)) {
		out = append(out, b.Endpoint)
	}
	return out
}

func routePort(rt *models.Route, app *models.Application) int {
	port := rt.TargetPort
	if port == 0 {
		port = app.Port
	}
	if port == 0 {
		port = 80
	}
	return port
}

// backendsFor builds the central proxy's upstreams for an app's route: the app's
// DNS alias where the gateway can reach it, otherwise a published host port on its
// node (see useAliasUpstream).
func (s *Service) backendsFor(app *models.Application, port int) []proxy.Backend {
	srv, _ := s.servers.FindByID(app.ServerID)
	if s.useAliasUpstream(srv) {
		return aliasBackends(app, port, portScheme(app, port))
	}
	// The host port for a port-forward node is a control-plane resource: it is
	// auto-provisioned (and auto-approved) when an app is attached to a route, so
	// admins never manage these bindings manually.
	hp, _ := s.ensureRemotePort(app, srv, port)
	if hp > 0 && strings.TrimSpace(srv.Address) != "" {
		return []proxy.Backend{{Endpoint: fmt.Sprintf("%s://%s:%d", portScheme(app, port), srv.Address, hp)}}
	}
	// No host port allocated or no node address — no usable upstream.
	return nil
}

// useAliasUpstream reports whether the gateway can dial the app by its DNS alias
// rather than a published host port on its node.
//
// True for the local node and for edge-gateway nodes (each already shares a network
// with the gateway that serves it), and for a port-forward node once cluster mode is
// on: the app then sits on the shared ingress overlay, which the central gateway has
// joined, so its alias resolves from anywhere in the cluster and no host port is
// published at all.
//
// Canary weighting depends on this. A host-port upstream can only name one
// container, so it always sends 100% to the stable release; only an alias upstream
// can carry the weighted split (see aliasBackends).
func (s *Service) useAliasUpstream(srv *models.Server) bool {
	return !isRemotePortForward(srv) || s.clusterOn()
}

// isRemotePortForward reports whether srv is a remote node reached by publishing
// host ports (the auto-port-forward case).
func isRemotePortForward(srv *models.Server) bool {
	return srv != nil && !srv.IsLocal && srv.Connectivity == models.ConnectivityPortForward
}

// privateBindIP returns the node's address to publish managed ports on when it
// is an IPv4 address, so ingress stays on the private interface. For a hostname
// or non-IPv4 address it returns "" (publish on all interfaces; the node must be
// firewalled so only the manager can reach the host-port range).
func privateBindIP(srv *models.Server) string {
	if ip := net.ParseIP(strings.TrimSpace(srv.Address)); ip != nil && ip.To4() != nil {
		return ip.String()
	}
	return ""
}

// aliasBackends builds node-local DNS-alias upstreams (control-plane Goma for the
// local node, or the node's own gateway for edge-gateway nodes), with canary
// weighting. scheme (http|https) is the app port's application protocol.
func aliasBackends(app *models.Application, port int, scheme string) []proxy.Backend {
	stable := fmt.Sprintf("%s://%s:%d", scheme, node.AppAlias(app), port)
	w := app.CanaryWeight
	if app.CanaryReleaseID == nil || w <= 0 {
		return []proxy.Backend{{Endpoint: stable}}
	}
	if w > 100 {
		w = 100
	}
	return []proxy.Backend{
		{Endpoint: stable, Weight: 100 - w},
		{Endpoint: fmt.Sprintf("%s://%s:%d", scheme, node.CanaryAlias(app), port), Weight: w},
	}
}

// portScheme returns the application scheme (http|https) the app declared for a
// container port, defaulting to http when the port isn't declared. Used to build
// the Gateway backend URL so an HTTPS-only container is reached over https.
func portScheme(app *models.Application, port int) string {
	for i := range app.Ports {
		if app.Ports[i].ContainerPort == port && app.Ports[i].Scheme == "https" {
			return "https"
		}
	}
	return "http"
}

// hostPort returns the approved host port published for an app's container port.
func (s *Service) hostPort(appID uint, containerPort int) int {
	bindings, err := s.ports.ListApprovedByApp(appID)
	if err != nil {
		return 0
	}
	for _, b := range bindings {
		if b.ContainerPort == containerPort {
			return b.HostPort
		}
	}
	return 0
}

// ensureRemotePort returns the host port published for a port-forward node app's
// container port, auto-provisioning an approved binding the first time the app
// is attached to a route. Remote port bindings are control-plane-managed (the
// admin never requests them), so this allocates a free port in the configured
// range (per node) and approves it directly. Idempotent: reuses an existing
// binding. Returns (hostPort, created); created is true only when a new binding
// was provisioned (so the caller can trigger a publish/redeploy).
func (s *Service) ensureRemotePort(app *models.Application, srv *models.Server, containerPort int) (int, bool) {
	if hp := s.hostPort(app.ID, containerPort); hp > 0 {
		return hp, false
	}
	hp := s.allocateHostPort(app.ServerID)
	if hp == 0 {
		logger.Warn("host-port range exhausted; route has no upstream",
			"app", app.ID, "server", app.ServerID, "range", fmt.Sprintf("%d-%d", s.minPort, s.maxPort))
		return 0, false
	}
	b := &models.PortBinding{
		WorkspaceID:   app.WorkspaceID,
		ApplicationID: app.ID,
		ServerID:      app.ServerID,
		ContainerPort: containerPort,
		Protocol:      "tcp",
		HostPort:      hp,
		Status:        models.PortBindingApproved,
		Managed:       true,
		BindIP:        privateBindIP(srv),
		ReviewNote:    "Auto-provisioned for port-forward node ingress",
	}
	if err := s.ports.Create(b); err != nil {
		return 0, false
	}
	return hp, true
}

// allocateHostPort scans the configured host-port range for a port not already
// claimed on the given node, returning 0 when the range is exhausted. Host ports
// are per-node, so the same number can be reused across different nodes.
func (s *Service) allocateHostPort(serverID uint) int {
	for p := s.minPort; p <= s.maxPort; p++ {
		inUse, err := s.ports.HostPortInUse(serverID, p, "tcp", 0)
		if err != nil {
			return 0
		}
		if !inUse {
			return p
		}
	}
	return 0
}

// NodeBundle renders the Goma config a remote node's Gateway pulls over the HTTP
// provider: every middleware (routes reference them by name) plus only the
// routes for apps placed on that node, with node-local upstreams.
func (s *Service) NodeBundle(serverID uint) ([]proxy.RenderedRoute, []proxy.RenderedMiddleware, error) {
	mws, err := s.middlewares.ListAll()
	if err != nil {
		return nil, nil, err
	}
	renderedMw := renderMiddlewares(mws)

	apps, err := s.apps.ListByServer(serverID)
	if err != nil {
		return nil, nil, err
	}
	// Verified-domain gate, same as the central sync. Apps on a node can span
	// workspaces, so cache each workspace's domains (and privilege) as we go.
	gate := s.domains != nil
	domainCache := map[uint][]models.Domain{}
	privCache := map[uint]bool{}
	now := time.Now()
	var renderedRoutes []proxy.RenderedRoute
	for i := range apps {
		app := &apps[i]
		if app.CurrentReleaseID == nil {
			continue
		}
		// Cluster (service) apps route via their overlay service VIP, not a
		// node-local container alias; skip them in the node bundle for now.
		if app.RuntimeKind == models.RuntimeService {
			continue
		}
		domains, ok := domainCache[app.WorkspaceID]
		if !ok {
			domains = s.workspaceDomains(app.WorkspaceID)
			domainCache[app.WorkspaceID] = domains
		}
		privileged, ok := privCache[app.WorkspaceID]
		if !ok {
			privileged = s.privileged(app.WorkspaceID)
			privCache[app.WorkspaceID] = privileged
		}
		routes, err := s.routes.ListByApp(app.ID)
		if err != nil {
			return nil, nil, err
		}
		for j := range routes {
			rt := &routes[j]
			serve, status, reason := routeServeState(rt, domains, gate, privileged)
			// Disabled routes are still emitted (with enabled:false) so the node's
			// gateway stops serving them rather than relying on the route vanishing
			// from the bundle.
			rr := proxy.RenderedRoute{
				ID: rt.ID, WorkspaceID: rt.WorkspaceID, Name: rt.Name, Path: rt.Path, Hosts: rt.Hosts, Methods: rt.Methods,
				Rewrite:      rt.Rewrite,
				Middlewares:  rt.Middlewares,
				Backends:     aliasBackends(app, routePort(rt, app), portScheme(app, routePort(rt, app))),
				TLSProvider:  rt.TLSProvider,
				TLSNone:      rt.TLSMode == models.RouteTLSNone,
				Disabled:     !serve,
				AdvancedYAML: rt.AdvancedConfig,
			}
			if pair, ok := s.certPair(rt); ok {
				rr.Certs = []proxy.CertPair{pair}
			}
			renderedRoutes = append(renderedRoutes, rr)
			// Edge routes are excluded from the central sync, so the node bundle is
			// their only status source — record what the node will serve.
			s.persistRouteStatus(rt, status, reason, now)
		}
	}
	return renderedRoutes, renderedMw, nil
}

// certPair resolves a custom-TLS route's certificate for proxy rendering from
// the stored certificate it references. Returns false when no usable cert is
// available.
func (s *Service) certPair(rt *models.Route) (proxy.CertPair, bool) {
	if rt.TLSMode != models.RouteTLSCustom || rt.CertificateID == nil || s.certs == nil {
		return proxy.CertPair{}, false
	}
	cert, key, err := s.certs.Resolve(rt.WorkspaceID, *rt.CertificateID)
	if err != nil || cert == "" {
		return proxy.CertPair{}, false
	}
	return proxy.CertPair{CertPEM: cert, KeyPEM: key}, true
}

// applyCert validates a route's certificate selection: custom TLS requires a
// stored certificate reference; other modes carry none.
func (s *Service) applyCert(rt *models.Route, _ Input) error {
	if rt.TLSMode != models.RouteTLSCustom {
		rt.CertificateID = nil // only custom TLS carries a cert reference
		return nil
	}
	if rt.CertificateID == nil {
		return ErrCertRequired
	}
	return nil
}

func strip(rt *models.Route) *models.Route {
	rt.HasCustomCert = rt.CertificateID != nil
	return rt
}

func defaultPath(p string) string {
	if strings.TrimSpace(p) == "" {
		return "/"
	}
	return p
}

func defaultTLS(m models.RouteTLSMode) models.RouteTLSMode {
	if m == "" {
		return models.RouteTLSACME
	}
	return m
}

// IDByUID resolves a route's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.routes.IDByUID(uid) }
