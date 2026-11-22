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

package route

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/mwcatalog"
)

type Flow struct {
	Route    FlowRoute        `json:"route"`
	App      FlowApp          `json:"app"`
	Hosts    []FlowHost       `json:"hosts"`
	TLS      FlowTLS          `json:"tls"`
	Gateway  FlowGateway      `json:"gateway"`
	Chain    []FlowMiddleware `json:"chain"`
	Siblings []FlowSibling    `json:"siblings"`
	Backends []FlowBackend    `json:"backends"`
}

type FlowRoute struct {
	ID                uint     `json:"id"`
	Name              string   `json:"name"`
	DisplayName       string   `json:"display_name,omitempty"`
	Status            string   `json:"status"`
	StatusReason      string   `json:"status_reason,omitempty"`
	Enabled           bool     `json:"enabled"`
	Generated         bool     `json:"generated"`
	Path              string   `json:"path"`
	Rewrite           string   `json:"rewrite,omitempty"`
	Methods           []string `json:"methods"`
	ExploitProtection bool     `json:"exploit_protection"`
	HasAdvancedConfig bool     `json:"has_advanced_config"`
	// Serving is whether the gateway answers for the route now; ServingReason says why not.
	Serving       bool            `json:"serving"`
	ServingReason string          `json:"serving_reason,omitempty"`
	Suspended     bool            `json:"suspended"`
	Maintenance   FlowMaintenance `json:"maintenance"`
}

type FlowMaintenance struct {
	Enabled    bool `json:"enabled"`
	StatusCode int  `json:"status_code"`
	HasMessage bool `json:"has_message"`
	HasHTML    bool `json:"has_html"`
}

type FlowApp struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	RuntimeKind string `json:"runtime_kind"`
	Port        int    `json:"port"`
	Scheme      string `json:"scheme"`
	Deployed    bool   `json:"deployed"`
}

type FlowHost struct {
	Host       string `json:"host"`
	Domain     string `json:"domain,omitempty"`
	Registered bool   `json:"registered"`
	Verified   bool   `json:"verified"`
	Banned     bool   `json:"banned"`
	// External is a generated host under the cluster's external-access domain, which no workspace domain gates.
	External bool `json:"external,omitempty"`
	// CertCovers is set only for a stored certificate: whether its names cover this host.
	CertCovers *bool `json:"cert_covers,omitempty"`
}

type FlowTLS struct {
	// Mode is acme (issued by the gateway), custom (a workspace certificate), external (the cluster's generated-URL certificate) or none.
	Mode            string     `json:"mode"`
	Provider        string     `json:"provider,omitempty"`
	CertificateID   *uint      `json:"certificate_id,omitempty"`
	CertificateName string     `json:"certificate_name,omitempty"`
	NotAfter        *time.Time `json:"not_after,omitempty"`
}

type FlowGateway struct {
	// Kind is platform (the control plane's gateway), cluster (a cluster's own gateway) or edge (a node's gateway).
	Kind       string `json:"kind"`
	ServerID   uint   `json:"server_id,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	Cluster    string `json:"cluster,omitempty"`
}

type FlowMiddleware struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`
	DisplayName string   `json:"display_name,omitempty"`
	Category    string   `json:"category,omitempty"`
	EarlyStatus []int    `json:"early_status"`
	Paths       []string `json:"paths"`
	// Missing means the route names a middleware the workspace no longer has; the gateway skips it.
	Missing bool `json:"missing,omitempty"`
}

type FlowSibling struct {
	RouteID     uint     `json:"route_id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	Path        string   `json:"path"`
	AppID       uint     `json:"app_id"`
	AppName     string   `json:"app_name,omitempty"`
	SharedHosts []string `json:"shared_hosts"`
	// Relation: narrower (it wins under its longer path), broader (this route wins under its path), separate.
	Relation string `json:"relation"`
}

type FlowBackend struct {
	Role      string                   `json:"role"`
	Endpoint  string                   `json:"endpoint"`
	Weight    int                      `json:"weight"`
	Match     []models.CanaryMatchRule `json:"match,omitempty"`
	Exclusive bool                     `json:"exclusive,omitempty"`
	Priority  int                      `json:"priority,omitempty"`
	Release   *FlowRelease             `json:"release,omitempty"`
	Health    *models.ReleaseHealth    `json:"health,omitempty"`
}

type FlowRelease struct {
	ID        uint      `json:"id"`
	Version   int       `json:"version"`
	Image     string    `json:"image"`
	ImageTag  string    `json:"image_tag,omitempty"`
	Digest    string    `json:"digest,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ReleaseHealthReader reports a release's runtime state. Implemented by services/application.
type ReleaseHealthReader interface {
	ReleaseHealth(ctx context.Context, app *models.Application, rel *models.Release) models.ReleaseHealth
}

// SetReleaseHealth wires live replica state into Flow (nil-safe; nil = backends carry no health).
func (s *Service) SetReleaseHealth(r ReleaseHealthReader) { s.health = r }

type certificateGetter interface {
	Get(workspaceID, id uint) (*models.Certificate, error)
}

// Flow builds from the same pieces SyncWorkspaceProxy renders, so the drawing matches what the gateway was given.
func (s *Service) Flow(ctx context.Context, workspaceID, id uint) (*Flow, error) {
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	app, err := s.apps.FindByID(rt.ApplicationID)
	if err != nil {
		return nil, err
	}
	in := flowInput{
		rt: rt, app: app, domains: s.workspaceDomains(workspaceID), gated: s.domains != nil,
		privileged: s.privileged(workspaceID), suspended: s.suspended != nil && s.suspended(workspaceID),
		tls: s.flowTLS(rt, app), gateway: s.flowGateway(app), releases: map[uint]*models.Release{},
	}
	if in.tls.Mode == "custom" && rt.CertificateID != nil {
		if g, ok := s.certs.(certificateGetter); ok {
			in.cert, _ = g.Get(workspaceID, *rt.CertificateID)
		}
	}
	if len(rt.Middlewares) > 0 {
		if in.middlewares, err = s.middlewares.ListByWorkspace(workspaceID); err != nil {
			return nil, err
		}
	}
	if in.routes, err = s.routes.ListByWorkspace(workspaceID); err != nil {
		return nil, err
	}
	in.appNames = s.appNames(workspaceID)
	for _, relID := range []*uint{app.CurrentReleaseID, app.CanaryReleaseID} {
		if relID != nil {
			if r, err := s.releases.FindByID(*relID); err == nil {
				in.releases[r.ID] = r
			}
		}
	}
	if s.health != nil {
		in.health = func(rel *models.Release) *models.ReleaseHealth {
			h := s.health.ReleaseHealth(ctx, app, rel)
			return &h
		}
	}
	return buildFlow(in), nil
}

type flowInput struct {
	rt          *models.Route
	app         *models.Application
	domains     []models.Domain
	gated       bool
	privileged  bool
	suspended   bool
	tls         FlowTLS
	cert        *models.Certificate
	gateway     FlowGateway
	middlewares []models.Middleware
	routes      []models.Route
	appNames    map[uint]string
	releases    map[uint]*models.Release
	health      func(rel *models.Release) *models.ReleaseHealth
}

func buildFlow(in flowInput) *Flow {
	rt, app := in.rt, in.app
	port := routePort(rt, app)
	f := &Flow{
		Route: FlowRoute{
			ID: rt.ID, Name: rt.Name, DisplayName: rt.DisplayName, Status: string(rt.Status), StatusReason: rt.StatusReason,
			Enabled: rt.Enabled, Generated: rt.Generated, Path: defaultPath(rt.Path), Rewrite: rt.Rewrite,
			Methods: nonNil(rt.Methods), ExploitProtection: rt.ExploitProtection,
			HasAdvancedConfig: strings.TrimSpace(rt.AdvancedConfig) != "",
			Suspended:         in.suspended,
			Maintenance: FlowMaintenance{
				Enabled: rt.Maintenance.Enabled, StatusCode: rt.Maintenance.StatusCode,
				HasMessage: rt.Maintenance.Message != "", HasHTML: rt.Maintenance.HTML != "",
			},
		},
		App: FlowApp{
			ID: app.ID, Name: app.Name, Status: string(app.Status), RuntimeKind: string(app.RuntimeKind),
			Port: port, Scheme: portScheme(app, port), Deployed: app.CurrentReleaseID != nil,
		},
		Hosts:    []FlowHost{},
		TLS:      in.tls,
		Gateway:  in.gateway,
		Chain:    flowChain(rt.Middlewares, in.middlewares),
		Siblings: flowSiblings(rt, in.routes, in.appNames),
		Backends: flowBackends(app, port, in.releases, in.health),
	}
	if f.Route.Maintenance.Enabled && f.Route.Maintenance.StatusCode == 0 {
		f.Route.Maintenance.StatusCode = 503
	}
	serve, _, reason := routeServeState(rt, in.domains, in.gated, in.privileged)
	if serve && !f.App.Deployed {
		serve, reason = false, "app not deployed"
	}
	f.Route.Serving, f.Route.ServingReason = serve, reason

	if in.cert != nil {
		f.TLS.CertificateName = in.cert.Name
		na := in.cert.NotAfter
		f.TLS.NotAfter = &na
	}
	for _, h := range rt.Hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		fh := FlowHost{Host: h, External: rt.Generated}
		if d := matchDomain(h, in.domains); d != nil && !rt.Generated {
			fh.Domain, fh.Registered, fh.Verified, fh.Banned = d.Name, true, d.Verified, d.Banned
		}
		if in.cert != nil {
			covers := certCovers(in.cert, h)
			fh.CertCovers = &covers
		}
		f.Hosts = append(f.Hosts, fh)
	}
	return f
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Service) flowTLS(rt *models.Route, app *models.Application) FlowTLS {
	switch {
	case rt.TLSMode == models.RouteTLSNone:
		return FlowTLS{Mode: "none"}
	case rt.TLSMode == models.RouteTLSCustom && rt.CertificateID != nil:
		return FlowTLS{Mode: "custom", CertificateID: rt.CertificateID}
	case rt.Generated && s.externalCerts != nil:
		if _, _, ok := s.externalCerts.ExternalCertificatePair(app.ClusterID); ok {
			return FlowTLS{Mode: "external"}
		}
	}
	return FlowTLS{Mode: "acme", Provider: rt.TLSProvider}
}

func certCovers(cert *models.Certificate, host string) bool {
	names := append([]string{cert.CommonName}, cert.DNSNames...)
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == host {
			return true
		}
		// A wildcard covers exactly one label: *.example.com covers a.example.com, not a.b.example.com.
		if rest, ok := strings.CutPrefix(n, "*."); ok {
			if i := strings.IndexByte(host, '.'); i > 0 && host[i+1:] == rest {
				return true
			}
		}
	}
	return false
}

func (s *Service) flowGateway(app *models.Application) FlowGateway {
	if c, ok := s.clusterGateway(app.ClusterID); ok {
		g := FlowGateway{Kind: "cluster", ServerID: c.IngressNode(), Cluster: c.DisplayName}
		if g.Cluster == "" {
			g.Cluster = c.Name
		}
		if srv, err := s.servers.FindByID(g.ServerID); err == nil {
			g.ServerName = srv.Name
		}
		return g
	}
	if id, ok := s.gatewayServer(app); ok {
		g := FlowGateway{Kind: "edge", ServerID: id}
		if srv, err := s.servers.FindByID(id); err == nil {
			g.ServerName = srv.Name
		}
		return g
	}
	return FlowGateway{Kind: "platform"}
}

// Rules are read for non-secret keys only (permanent, statusCode); nothing of a rule is returned.
func flowChain(names []string, mws []models.Middleware) []FlowMiddleware {
	out := []FlowMiddleware{}
	byName := make(map[string]*models.Middleware, len(mws))
	for i := range mws {
		byName[mws[i].Name] = &mws[i]
	}
	for _, n := range names {
		m, ok := byName[n]
		if !ok {
			out = append(out, FlowMiddleware{Name: n, Missing: true, EarlyStatus: []int{}, Paths: []string{}})
			continue
		}
		fm := FlowMiddleware{Name: n, Type: m.Type, EarlyStatus: mwcatalog.EarlyStatusOf(m.Type, m.Rule), Paths: nonNil(m.Paths)}
		if d, ok := mwcatalog.Get(m.Type); ok {
			fm.DisplayName, fm.Category = d.DisplayName, string(d.Category)
		}
		if fm.EarlyStatus == nil {
			fm.EarlyStatus = []int{}
		}
		out = append(out, fm)
	}
	return out
}

func flowSiblings(rt *models.Route, routes []models.Route, appNames map[uint]string) []FlowSibling {
	out := []FlowSibling{}
	mine := map[string]bool{}
	for _, h := range rt.Hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			mine[h] = true
		}
	}
	myPath := defaultPath(rt.Path)
	for i := range routes {
		o := &routes[i]
		if o.ID == rt.ID {
			continue
		}
		var shared []string
		for _, h := range o.Hosts {
			if h = strings.ToLower(strings.TrimSpace(h)); mine[h] {
				shared = append(shared, h)
			}
		}
		if len(shared) == 0 {
			continue
		}
		p := defaultPath(o.Path)
		out = append(out, FlowSibling{
			RouteID: o.ID, Name: o.Name, DisplayName: o.DisplayName, Path: p,
			AppID: o.ApplicationID, AppName: appNames[o.ApplicationID], SharedHosts: shared,
			Relation: pathRelation(myPath, p),
		})
	}
	return out
}

func (s *Service) appNames(workspaceID uint) map[uint]string {
	out := map[uint]string{}
	apps, err := s.apps.ListByWorkspace(workspaceID)
	if err != nil {
		return out
	}
	for i := range apps {
		out[apps[i].ID] = apps[i].Name
	}
	return out
}

// pathRelation says which of two routes on one host wins where; the gateway picks the longest matching prefix.
func pathRelation(mine, other string) string {
	switch {
	case len(other) > len(mine) && underPath(other, mine):
		return "narrower"
	case len(mine) > len(other) && underPath(mine, other):
		return "broader"
	}
	return "separate"
}

func underPath(p, prefix string) bool {
	prefix = path.Clean("/" + prefix)
	p = path.Clean("/" + p)
	return prefix == "/" || p == prefix || strings.HasPrefix(p, prefix+"/")
}

func flowBackends(app *models.Application, port int, releases map[uint]*models.Release, health func(*models.Release) *models.ReleaseHealth) []FlowBackend {
	out := []FlowBackend{}
	if app.CurrentReleaseID == nil {
		return out
	}
	backends := gatewayBackends(app, port)
	for i, b := range backends {
		fb := FlowBackend{Role: "stable", Endpoint: b.Endpoint, Weight: b.Weight}
		relID := app.CurrentReleaseID
		if i > 0 {
			fb.Role = "canary"
			relID = app.CanaryReleaseID
			fb.Exclusive, fb.Priority = b.Exclusive, b.Priority
			for _, m := range b.Match {
				fb.Match = append(fb.Match, models.CanaryMatchRule{Source: m.Source, Name: m.Name, Operator: m.Operator, Value: m.Value})
			}
		}
		if len(backends) == 1 {
			fb.Weight = 100
		}
		var rel *models.Release
		if relID != nil {
			rel = releases[*relID]
		}
		if rel != nil {
			fb.Release = &FlowRelease{ID: rel.ID, Version: rel.Version, Image: rel.Image, ImageTag: rel.ImageTag, Digest: rel.Digest, CreatedAt: rel.CreatedAt}
		}
		if health != nil {
			fb.Health = health(rel)
		}
		out = append(out, fb)
	}
	return out
}
