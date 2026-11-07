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

// Package route manages Goma Gateway routes bound to applications and reconciles them into the proxy provider.
package route

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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
)

var (
	ErrNameRequired        = errors.New("route name is required")
	ErrInvalidName         = errors.New("name must be lowercase letters, digits and hyphens (e.g. my-api)")
	ErrHostRequired        = errors.New("at least one host is required")
	ErrMaintenanceStatus   = errors.New("maintenance status code must be between 400 and 599")
	ErrMaintenanceMessage  = errors.New("maintenance message is too long (max 1024 characters)")
	ErrNameTaken           = errors.New("a route with this name already exists")
	ErrAppRequired         = errors.New("application not found in workspace")
	ErrNotFound            = errors.New("route not found")
	ErrCertRequired        = errors.New("custom TLS requires a stored certificate")
	ErrInvalidYAML         = errors.New("advanced config is not valid YAML")
	ErrDomainNotRegistered = errors.New("no matching domain is registered in this workspace; add the domain first")
	ErrDomainBanned        = errors.New("this domain has been banned by a platform administrator")
	ErrHostTaken           = errors.New("hostname is already used by another route")
	ErrAdvancedTLSCert     = errors.New("set TLS via the route's TLS mode and a managed certificate, not an inline tls certificate in advanced config")
	ErrMiddlewareRequired  = errors.New("middleware name is required")
	ErrMiddlewareNotFound  = errors.New("middleware not found in this workspace")
	ErrMiddlewareDuplicate = errors.New("the same middleware is listed twice")
)

const maintenanceMessageMax = 1024

type CertResolver interface {
	Resolve(workspaceID, id uint) (certPEM, keyPEM string, err error)
}

type Service struct {
	routes      *repositories.RouteRepository
	middlewares *repositories.MiddlewareRepository
	apps        *repositories.ApplicationRepository
	releases    *repositories.ReleaseRepository
	servers     *repositories.ServerRepository
	proxy       proxy.Manager
	certs       CertResolver
	attacher    ProxyAttacher
	domains     DomainLister
	wsPolicy    WorkspacePolicy
	// suspended reports a workspace a platform admin suspended; its routes then serve a notice.
	suspended func(workspaceID uint) bool
	dnsAddr   DNSAddresser
	reloader  EdgeReloader
	cluster   ClusterCap
	external  ExternalDomains
	// externalCerts serves a cluster's generated URLs from one certificate; nil leaves each its own.
	externalCerts ExternalCertificates
}

// ClusterCap reports whether a gateway of its own serves a cluster. Implemented by services/cluster.
type ClusterCap interface {
	OwnGateway(clusterID uint) (*models.Cluster, bool)
	// IngressAddress is where a cluster's public DNS records point; id 0 is the default cluster.
	IngressAddress(clusterID uint) models.PublicAddress
	IsDefaultCluster(clusterID uint) bool
}

// SetCluster wires cluster gateways (nil-safe; nil = every cluster is served as the default one).
func (s *Service) SetCluster(c ClusterCap) { s.cluster = c }

// EdgeReloader makes edge gateways pull config now instead of on their next poll; best-effort and optional.
type EdgeReloader interface {
	ReloadServers(ctx context.Context, serverIDs []uint)
}

func (s *Service) SetEdgeReloader(r EdgeReloader) { s.reloader = r }

// DNSAddresser keeps an app's public DNS records in sync with routed hosts under provider-connected domains.
type DNSAddresser interface {
	ReconcileAppAddresses(ctx context.Context, workspaceID, appID uint, hosts []string, target models.PublicAddress) error
}

// SetDNSAddresser wires app-address DNS automation (nil-safe; nil = off).
func (s *Service) SetDNSAddresser(a DNSAddresser) { s.dnsAddr = a }

// DomainLister bounds user-created route hosts; generated external-access routes bypass it.
type DomainLister interface {
	ListByWorkspace(workspaceID uint) ([]models.Domain, error)
}

// SetDomains wires the domain registry used to gate route hostnames.
func (s *Service) SetDomains(d DomainLister) { s.domains = d }

// WorkspacePolicy reports whether a workspace may expose routes on registered but unverified domains.
type WorkspacePolicy interface {
	IsPrivileged(workspaceID uint) (bool, error)
}

// SetWorkspacePolicy wires the privileged-workspace lookup (nil-safe).
func (s *Service) SetWorkspacePolicy(p WorkspacePolicy) { s.wsPolicy = p }

func (s *Service) SetSuspended(f func(workspaceID uint) bool) { s.suspended = f }

// Never carries the admin's reason, which is for the workspace's members, not the public.
const suspendedNotice = "This site is temporarily unavailable. If you run it, sign in to your Miabi workspace for details."

// Turns the route into a 503 notice whatever its maintenance setting, without touching the stored route.
func (s *Service) applySuspension(rr *proxy.RenderedRoute) {
	if s.suspended != nil && s.suspended(rr.WorkspaceID) {
		rr.Maintenance = &proxy.RouteMaintenance{Enabled: true, StatusCode: 503, Message: suspendedNotice}
	}
}

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

// CoveringDomain returns the workspace domain a host falls under, or nil when none or checks aren't wired.
func (s *Service) CoveringDomain(workspaceID uint, host string) (*models.Domain, error) {
	if s.domains == nil {
		return nil, nil
	}
	domains, err := s.domains.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	return matchDomain(strings.ToLower(strings.TrimSpace(host)), domains), nil
}

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

// DNS is case-insensitive, so hosts are stored and compared canonically.
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

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	return p
}

// A host routed by another workspace is rejected to prevent cross-tenant hijacking; same workspace needs a new path.
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

func hostUnderDomain(host string, domains []models.Domain) bool {
	for i := range domains {
		name := strings.ToLower(domains[i].Name)
		if host == name || strings.HasSuffix(host, "."+name) {
			return true
		}
	}
	return false
}

// A wildcard domain matches by name like any other: ownership is proven once for the whole zone.
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

func hasHost(hosts []string) bool {
	for _, h := range hosts {
		if strings.TrimSpace(h) != "" {
			return true
		}
	}
	return false
}

// Serve only when enabled and every host's domain is unbanned and verified (unless privileged); generated routes exempt.
func routeServeState(rt *models.Route, domains []models.Domain, gate, privileged bool) (serve bool, status models.RouteStatus, reason string) {
	if !rt.Enabled {
		return false, models.RouteStatusOffline, "disabled"
	}
	if rt.Generated {
		return true, models.RouteStatusLive, ""
	}

	if !hasHost(rt.Hosts) {
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

// Skips unchanged writes so node pulls and re-syncs don't churn the row.
func (s *Service) persistRouteStatus(rt *models.Route, status models.RouteStatus, reason string, syncedAt time.Time) {
	if rt.Status == status && rt.StatusReason == reason {
		return
	}
	if err := s.routes.UpdateStatus(rt.ID, status, reason, syncedAt); err != nil {
		logger.Warn("failed to persist route status", "route", rt.ID, "err", err)
	}
}

// ProxyAttacher attaches only route-exposed apps' containers to the shared reverse-proxy network.
type ProxyAttacher interface {
	ReconcileProxyAttachment(ctx context.Context, appID uint, attached bool) error
}

// SetCertResolver wires the certificate store used to resolve custom-cert routes.
func (s *Service) SetCertResolver(r CertResolver) { s.certs = r }

// SetProxyAttacher wires the proxy-network reconciler used when routes change.
func (s *Service) SetProxyAttacher(a ProxyAttacher) { s.attacher = a }

func (s *Service) RoutesUsingCertificate(workspaceID, certID uint) ([]models.Route, error) {
	return s.routes.ListByCertificate(workspaceID, certID)
}

func NewService(
	routes *repositories.RouteRepository,
	middlewares *repositories.MiddlewareRepository,
	apps *repositories.ApplicationRepository,
	releases *repositories.ReleaseRepository,
	servers *repositories.ServerRepository,
	proxyMgr proxy.Manager,
) *Service {
	return &Service{routes: routes, middlewares: middlewares, apps: apps, releases: releases, servers: servers, proxy: proxyMgr}
}

type Input struct {
	Name          string
	DisplayName   string
	ApplicationID uint
	Path          string
	Hosts         []string
	Methods       []string
	Middlewares   []string
	Rewrite       string
	TargetPort    int
	TLSMode       models.RouteTLSMode

	TLSProvider       *string
	AdvancedConfig    string // raw Goma route YAML; supersedes structured fields
	CertificateID     *uint  // stored certificate (required for custom TLS)
	Enabled           *bool
	ExploitProtection *bool
	Maintenance       *models.RouteMaintenance
	Metadata          models.Metadata
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
	in.Hosts = normalizeHosts(in.Hosts)

	if len(in.Hosts) == 0 {
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
	// Validated here too: an unchecked name would be stored and render a config naming a nonexistent middleware.
	chain, err := normalizeChain(in.Middlewares, func(n string) (bool, error) {
		return s.middlewares.ExistsByName(workspaceID, n)
	})
	if err != nil {
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
		Middlewares:    chain,
		Rewrite:        in.Rewrite,
		TargetPort:     in.TargetPort,
		TLSMode:        defaultTLS(in.TLSMode),
		TLSProvider:    trimPtr(in.TLSProvider),
		AdvancedConfig: strings.TrimSpace(in.AdvancedConfig),
		CertificateID:  in.CertificateID,
		Enabled:        in.Enabled == nil || *in.Enabled,
		Metadata:       in.Metadata,

		ExploitProtection: in.ExploitProtection != nil && *in.ExploitProtection,
	}
	mt, err := normalizeMaintenance(in.Maintenance)
	if err != nil {
		return nil, err
	}
	rt.Maintenance = mt
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
	// apply/GitOps passes the app every reconcile so a stale link converges; the UI passes 0 to keep the link.
	if in.ApplicationID != 0 && in.ApplicationID != rt.ApplicationID {
		app, aerr := s.apps.FindInWorkspace(workspaceID, in.ApplicationID)
		if aerr != nil {
			return nil, ErrAppRequired
		}
		rt.ApplicationID = app.ID
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
	// Generated routes manage their own hosts, so only user routes are gated on domain and uniqueness.
	if !rt.Generated {
		in.Hosts = normalizeHosts(in.Hosts)
		if len(in.Hosts) == 0 {
			return nil, ErrHostRequired
		}
		if err := s.validateHosts(workspaceID, in.Hosts); err != nil {
			return nil, err
		}
		if err := s.ensureHostsAvailable(workspaceID, in.Hosts, rt.Path, rt.ID); err != nil {
			return nil, err
		}
	}
	chain, err := normalizeChain(in.Middlewares, func(n string) (bool, error) {
		return s.middlewares.ExistsByName(workspaceID, n)
	})
	if err != nil {
		return nil, err
	}
	rt.Hosts = in.Hosts
	rt.Methods = in.Methods
	rt.Middlewares = chain
	rt.Rewrite = in.Rewrite
	rt.TargetPort = in.TargetPort
	if in.TLSMode != "" {
		rt.TLSMode = in.TLSMode
	}
	if in.TLSProvider != nil {
		rt.TLSProvider = strings.TrimSpace(*in.TLSProvider)
	}
	if err := validateAdvanced(in.AdvancedConfig); err != nil {
		return nil, err
	}
	rt.AdvancedConfig = strings.TrimSpace(in.AdvancedConfig)
	rt.CertificateID = in.CertificateID
	if in.Enabled != nil {
		rt.Enabled = *in.Enabled
	}
	if in.ExploitProtection != nil {
		rt.ExploitProtection = *in.ExploitProtection
	}
	if in.Maintenance != nil {
		mt, err := normalizeMaintenance(in.Maintenance)
		if err != nil {
			return nil, err
		}
		rt.Maintenance = mt
	}
	// A UI edit passes nil so existing provenance labels are preserved.
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

func (s *Service) SetSecurity(ctx context.Context, workspaceID, id uint, exploitProtection *bool) (*models.Route, error) {
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if exploitProtection != nil {
		rt.ExploitProtection = *exploitProtection
	}
	if err := s.routes.Update(rt); err != nil {
		return nil, err
	}
	_ = s.SyncRoute(ctx, rt.ApplicationID)
	strip(rt)
	s.enrichDNS(rt)
	return rt, nil
}

func (s *Service) SetMaintenance(ctx context.Context, workspaceID, id uint, in models.RouteMaintenance) (*models.Route, error) {
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	mt, err := normalizeMaintenance(&in)
	if err != nil {
		return nil, err
	}
	rt.Maintenance = mt
	if err := s.routes.Update(rt); err != nil {
		return nil, err
	}
	_ = s.SyncRoute(ctx, rt.ApplicationID)
	strip(rt)
	s.enrichDNS(rt)
	return rt, nil
}

// AttachMiddleware works on generated external-access routes too, unlike a full Update.
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

// The returned order is the order the gateway will run the chain in.
func normalizeChain(names []string, exists func(string) (bool, error)) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			return nil, ErrMiddlewareRequired
		}
		if slices.Contains(out, n) {
			return nil, ErrMiddlewareDuplicate
		}
		ok, err := exists(n)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrMiddlewareNotFound
		}
		out = append(out, n)
	}
	return out, nil
}

// SetMiddlewares keeps order: the gateway runs middlewares in array order, so rate-limit vs basic-auth matters.
func (s *Service) SetMiddlewares(ctx context.Context, workspaceID, id uint, names []string) (*models.Route, error) {
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	out, err := normalizeChain(names, func(name string) (bool, error) {
		return s.middlewares.ExistsByName(workspaceID, name)
	})
	if err != nil {
		return nil, err
	}
	if slices.Equal(rt.Middlewares, out) {
		strip(rt)
		s.enrichDNS(rt)
		return rt, nil
	}
	rt.Middlewares = out
	if err := s.routes.Update(rt); err != nil {
		return nil, err
	}
	_ = s.SyncRoute(ctx, rt.ApplicationID)
	strip(rt)
	s.enrichDNS(rt)
	return rt, nil
}

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

	_ = s.SyncRoute(ctx, rt.ApplicationID)
	return nil
}

// A generated route is valid only while its port is primary or declared; user routes may target any port.
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

// RemoveAppRoutes deletes every route of a deleted app, from the database and the gateway.
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
			_ = s.dnsAddr.ReconcileAppAddresses(ctx, wsID, appID, nil, models.PublicAddress{})
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

// SyncRoute removes routes while the app has no active release, else upserts enabled routes.
func (s *Service) SyncRoute(ctx context.Context, appID uint) error {
	app, err := s.apps.FindByID(appID)
	if err != nil {
		return err
	}
	routes, err := s.routes.ListByApp(appID)
	if err != nil {
		return err
	}
	// A generated route must never outlive the port it fronts; user routes may target any port.
	kept, orphans := partitionGeneratedOrphans(app, routes)
	for i := range orphans {
		_ = s.routes.Delete(orphans[i].ID)
	}
	routes = kept

	if s.attacher != nil {
		_ = s.attacher.ReconcileProxyAttachment(ctx, appID, len(routes) > 0)
	}
	// Keep the app's public A/AAAA/CNAME records in sync with its routed hosts
	s.reconcileAppDNS(ctx, app, routes)
	return s.SyncWorkspaceProxy(ctx, app.WorkspaceID)
}

// Fail-closed: a middleware whose secrets can't be decrypted is skipped rather than rendering ciphertext.
func renderMiddlewares(mws []models.Middleware) []proxy.RenderedMiddleware {
	out := make([]proxy.RenderedMiddleware, 0, len(mws))
	for i := range mws {
		m := &mws[i]
		rule, err := mwcatalog.DecryptSecrets(m.Type, m.Rule)
		if err != nil {
			logger.Error("middleware: decrypt secret rule failed; skipping", "workspace", m.WorkspaceID, "middleware", m.Name, "error", err)
			continue
		}
		// A rule saved before Goma dropped a key would otherwise fail the whole workspace file.
		rule = mwcatalog.DropRetired(m.Type, rule)
		out = append(out, proxy.RenderedMiddleware{ID: m.ID, WorkspaceID: m.WorkspaceID, Name: m.Name, Type: m.Type, Paths: m.Paths, Rule: rule})
	}
	return out
}

// SyncWorkspaceProxy is the single central write path, so routes and their middlewares publish together.
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

	gate := s.domains != nil
	domains := s.workspaceDomains(workspaceID)
	privileged := s.privileged(workspaceID)
	var renderedRoutes []proxy.RenderedRoute

	var pending []routeStatus

	edgeServers := map[uint]struct{}{}
	extCerts := map[uint]externalCert{}
	for i := range apps {

		app, err := s.apps.FindByID(apps[i].ID)
		if err != nil {
			continue
		}
		if app.CurrentReleaseID == nil {
			continue
		}
		if id, ok := s.gatewayServer(app); ok {
			edgeServers[id] = struct{}{}
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
			rr := renderedRoute(rt, gatewayBackends(app, port), !serve)
			s.applySuspension(&rr)
			// A cert rides only a route the gateway will actually serve.
			if pair, ok := s.certPair(rt); ok && serve {
				rr.Certs = []proxy.CertPair{pair}
			} else if serve {
				s.withExternalCert(&rr, rt, app, extCerts)
			}
			renderedRoutes = append(renderedRoutes, rr)
			pending = append(pending, routeStatus{rt: rt, status: status, reason: reason})
		}
	}
	err = s.proxy.SyncWorkspace(ctx, workspaceID, renderedRoutes, renderedMw)
	s.commitRouteStatus(pending, err)
	// Detached so a slow or unreachable node never blocks the sync.
	if err == nil && s.reloader != nil && len(edgeServers) > 0 {
		ids := make([]uint, 0, len(edgeServers))
		for id := range edgeServers {
			ids = append(ids, id)
		}
		go s.reloader.ReloadServers(context.WithoutCancel(ctx), ids)
	}
	return err
}

type routeStatus struct {
	rt     *models.Route
	status models.RouteStatus
	reason string
}

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

// ResyncAllProxy runs at startup to populate per-workspace files, e.g. after upgrading from the per-resource layout.
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

func (s *Service) clusterGateway(clusterID uint) (*models.Cluster, bool) {
	if s.cluster == nil {
		return nil, false
	}
	return s.cluster.OwnGateway(clusterID)
}

// A remote swarm's ingress node serves its whole cluster; an edge node outside the default cluster serves its own.
func (s *Service) gatewayServer(app *models.Application) (uint, bool) {
	if c, ok := s.clusterGateway(app.ClusterID); ok {
		return c.IngressNode(), true
	}
	if app.RuntimeKind == models.RuntimeService {
		return 0, false
	}
	srv, err := s.servers.FindByID(app.ServerID)
	if err != nil || srv.IsLocal || srv.Connectivity != models.ConnectivityEdgeGateway || s.inDefaultCluster(srv.ClusterID) {
		return 0, false
	}
	return srv.ID, true
}

func (s *Service) inDefaultCluster(clusterID uint) bool {
	return s.cluster != nil && s.cluster.IsDefaultCluster(clusterID)
}

// gatewayBackends are the upstreams a gateway dials by alias: a service's VIP, or the app's containers.
func gatewayBackends(app *models.Application, port int) []proxy.Backend {
	scheme := portScheme(app, port)
	if app.RuntimeKind == models.RuntimeService {
		return []proxy.Backend{{Endpoint: fmt.Sprintf("%s://%s:%d", scheme, node.AppAlias(app), port)}}
	}
	return aliasBackends(app, port, scheme)
}

func (s *Service) reconcileAppDNS(ctx context.Context, app *models.Application, routes []models.Route) {
	if s.dnsAddr == nil {
		return
	}
	if err := s.dnsAddr.ReconcileAppAddresses(ctx, app.WorkspaceID, app.ID, routedHosts(routes), s.dnsTarget(app)); err != nil {
		logger.Warn("dns app-address reconcile failed", "app", app.ID, "error", err)
	}
}

// Apps the control-plane gateway serves use the default cluster's address.
func (s *Service) dnsTarget(app *models.Application) models.PublicAddress {
	if s.cluster == nil {
		return models.PublicAddress{}
	}
	clusterID := models.DefaultClusterID
	if c, ok := s.clusterGateway(app.ClusterID); ok {
		clusterID = c.ID
	} else if id, ok := s.gatewayServer(app); ok {
		srv, err := s.servers.FindByID(id)
		if err != nil {
			return models.PublicAddress{}
		}
		clusterID = srv.ClusterID
	}
	return s.cluster.IngressAddress(clusterID)
}

// routedHosts are the hosts of an app's enabled routes, the names its public DNS records cover.
func routedHosts(routes []models.Route) []string {
	hosts := make([]string, 0)
	for i := range routes {
		if routes[i].Enabled {
			hosts = append(hosts, routes[i].Hosts...)
		}
	}
	return hosts
}

func (s *Service) enrichDNS(rt *models.Route) {
	app, err := s.apps.FindByID(rt.ApplicationID)
	if err != nil {
		return
	}
	addr := s.dnsTarget(app)
	rt.DNSTarget, rt.DNSTargetIPv6, rt.DNSHostname = cmp.Or(addr.IPv4, addr.IPv6), addr.IPv6, addr.Hostname
	// A www host's record depends on whether its bare name is routed too, by this route or another of the app.
	routed := rt.Hosts
	if siblings, err := s.routes.ListByApp(app.ID); err == nil {
		routed = append(routedHosts(siblings), rt.Hosts...)
	}
	rt.DNSRecords = nil
	for _, h := range rt.Hosts {
		rt.DNSRecords = append(rt.DNSRecords, addr.AddressRecords(h, routed, false)...)
	}
	rt.Backends = s.displayBackends(app, routePort(rt, app))
}

func (s *Service) displayBackends(app *models.Application, port int) []string {
	backends := gatewayBackends(app, port)
	out := make([]string, 0, len(backends))
	for _, b := range backends {
		out = append(out, b.Endpoint)
	}
	return out
}

func normalizeMaintenance(in *models.RouteMaintenance) (models.RouteMaintenance, error) {
	if in == nil {
		return models.RouteMaintenance{}, nil
	}
	out := models.RouteMaintenance{
		Enabled:    in.Enabled,
		StatusCode: in.StatusCode,
		Message:    strings.TrimSpace(in.Message),
	}
	if out.StatusCode != 0 && (out.StatusCode < 400 || out.StatusCode > 599) {
		return models.RouteMaintenance{}, ErrMaintenanceStatus
	}
	if len(out.Message) > maintenanceMessageMax {
		return models.RouteMaintenance{}, ErrMaintenanceMessage
	}
	return out, nil
}

func renderedRoute(rt *models.Route, backends []proxy.Backend, disabled bool) proxy.RenderedRoute {
	rr := proxy.RenderedRoute{
		ID:                rt.ID,
		WorkspaceID:       rt.WorkspaceID,
		Name:              rt.Name,
		Path:              rt.Path,
		Hosts:             rt.Hosts,
		Methods:           rt.Methods,
		Rewrite:           rt.Rewrite,
		Middlewares:       rt.Middlewares,
		Backends:          backends,
		TLSProvider:       rt.TLSProvider,
		TLSNone:           rt.TLSMode == models.RouteTLSNone,
		Disabled:          disabled,
		AdvancedYAML:      rt.AdvancedConfig,
		ExploitProtection: rt.ExploitProtection,
	}
	if rt.Maintenance.Enabled {
		rr.Maintenance = &proxy.RouteMaintenance{
			Enabled:    true,
			StatusCode: rt.Maintenance.StatusCode,
			Message:    rt.Maintenance.Message,
		}
	}
	return rr
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

func aliasBackends(app *models.Application, port int, scheme string) []proxy.Backend {
	stable := fmt.Sprintf("%s://%s:%d", scheme, node.AppAlias(app), port)
	w := app.CanaryWeight
	if w > 100 {
		w = 100
	}
	if w < 0 {
		w = 0
	}
	rules := canaryMatchRules(app)
	if app.CanaryReleaseID == nil || (w <= 0 && !(app.CanaryExclusive && len(rules) > 0)) {
		return []proxy.Backend{{Endpoint: stable}}
	}
	canary := proxy.Backend{
		Endpoint: fmt.Sprintf("%s://%s:%d", scheme, node.CanaryAlias(app), port),
		Weight:   w,
		Match:    rules,
	}

	if len(rules) > 0 {
		canary.Exclusive = app.CanaryExclusive
		canary.Priority = app.CanaryPriority
	}
	return []proxy.Backend{
		{Endpoint: stable, Weight: 100 - w},
		canary,
	}
}

// Returns nil for an empty set so an ordinary canary renders unchanged.
func canaryMatchRules(app *models.Application) []proxy.MatchRule {
	if len(app.CanaryMatch) == 0 {
		return nil
	}
	out := make([]proxy.MatchRule, 0, len(app.CanaryMatch))
	for _, r := range app.CanaryMatch {
		out = append(out, proxy.MatchRule{Source: r.Source, Name: r.Name, Operator: r.Operator, Value: r.Value})
	}
	return out
}

// Defaults to http so an HTTPS-only container is reached over https only when declared.
func portScheme(app *models.Application, port int) string {
	for i := range app.Ports {
		if app.Ports[i].ContainerPort == port && app.Ports[i].Scheme == "https" {
			return "https"
		}
	}
	return "http"
}

// RouteNamesForNode gates analytics ingest: the workspace is read from the route name, so foreign ones claim others' traffic.
func (s *Service) RouteNamesForNode(serverID uint) (map[string]bool, error) {
	rendered, _, err := s.NodeBundle(serverID)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(rendered))
	for i := range rendered {
		names[proxy.GomaName(rendered[i].WorkspaceID, rendered[i].Name)] = true
	}
	return names, nil
}

// SyncCluster re-syncs the routes of every app in a cluster, so they follow its gateway to another node.
func (s *Service) SyncCluster(ctx context.Context, clusterID uint) {
	apps, err := s.apps.ListByCluster(clusterID)
	if err != nil {
		logger.Warn("cluster route resync failed", "cluster", clusterID, "error", err)
		return
	}
	for i := range apps {
		if err := s.SyncRoute(ctx, apps[i].ID); err != nil {
			logger.Warn("route resync after a gateway change failed", "app", apps[i].ID, "error", err)
		}
	}
}

// NodeBundle ships only referenced middlewares, since they render with decrypted secrets.
func (s *Service) NodeBundle(serverID uint) ([]proxy.RenderedRoute, []proxy.RenderedMiddleware, error) {
	apps, err := s.bundleApps(serverID)
	if err != nil {
		return nil, nil, err
	}
	// Apps on a node can span workspaces, so each workspace's domains and privilege are cached.
	gate := s.domains != nil
	domainCache := map[uint][]models.Domain{}
	privCache := map[uint]bool{}
	extCerts := map[uint]externalCert{}
	now := time.Now()
	var renderedRoutes []proxy.RenderedRoute
	for i := range apps {
		app := &apps[i]
		if app.CurrentReleaseID == nil {
			continue
		}
		if gw, ok := s.gatewayServer(app); !ok || gw != serverID {
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
			// Disabled routes are emitted with enabled:false so the gateway stops serving them.
			rr := renderedRoute(rt, gatewayBackends(app, routePort(rt, app)), !serve)
			s.applySuspension(&rr)
			// Only a served route carries its cert.
			if pair, ok := s.certPair(rt); ok && serve {
				rr.Certs = []proxy.CertPair{pair}
			} else if serve {
				s.withExternalCert(&rr, rt, app, extCerts)
			}
			renderedRoutes = append(renderedRoutes, rr)

			s.persistRouteStatus(rt, status, reason, now)
		}
	}

	// Goma rejects a config naming an undefined middleware, so disabled routes' middlewares ship too.
	mws, err := s.middlewares.ListAll()
	if err != nil {
		return nil, nil, err
	}
	renderedMw := renderMiddlewares(referencedMiddlewares(mws, renderedRoutes))
	return renderedRoutes, renderedMw, nil
}

// Matching is per workspace as well as name: two workspaces may each define "auth".
func referencedMiddlewares(mws []models.Middleware, routes []proxy.RenderedRoute) []models.Middleware {
	wanted := make(map[uint]map[string]bool, len(routes))
	for i := range routes {
		for _, name := range routes[i].Middlewares {
			ws := wanted[routes[i].WorkspaceID]
			if ws == nil {
				ws = map[string]bool{}
				wanted[routes[i].WorkspaceID] = ws
			}
			ws[name] = true
		}
	}
	out := make([]models.Middleware, 0, len(wanted))
	for i := range mws {
		if wanted[mws[i].WorkspaceID][mws[i].Name] {
			out = append(out, mws[i])
		}
	}
	return out
}

func (s *Service) bundleApps(serverID uint) ([]models.Application, error) {
	if srv, err := s.servers.FindByID(serverID); err == nil {
		if c, ok := s.clusterGateway(srv.ClusterID); ok && c.IngressNode() == serverID {
			return s.apps.ListByCluster(c.ID)
		}
	}
	return s.apps.ListByServer(serverID)
}

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

func trimPtr(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
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
