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
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/slug"
)

var ErrExternalAccessDisabled = errors.New("external access is off in this app's location (a platform admin sets the cluster's external domain)")

type ExternalDomains interface {
	ExternalAccess(clusterID uint) (domain, provider string)
}

// SetExternalDomains wires per-cluster external access; unset, external access is off everywhere.
func (s *Service) SetExternalDomains(d ExternalDomains) { s.external = d }

type ExternalCertificates interface {
	ExternalCertificatePair(clusterID uint) (certPEM, keyPEM string, ok bool)
}

// SetExternalCertificates wires per-cluster certificates; unset, each generated URL gets its own from certManager.
func (s *Service) SetExternalCertificates(c ExternalCertificates) { s.externalCerts = c }

type externalCert struct {
	pair proxy.CertPair
	ok   bool
}

// The provider is dropped so the gateway presents the cluster certificate instead of requesting one.
func (s *Service) withExternalCert(rr *proxy.RenderedRoute, rt *models.Route, app *models.Application, cache map[uint]externalCert) {
	if !rt.Generated || s.externalCerts == nil {
		return
	}
	ec, seen := cache[app.ClusterID]
	if !seen {
		cert, key, ok := s.externalCerts.ExternalCertificatePair(app.ClusterID)
		ec = externalCert{pair: proxy.CertPair{CertPEM: cert, KeyPEM: key}, ok: ok}
		cache[app.ClusterID] = ec
	}
	if ec.ok {
		rr.Certs = []proxy.CertPair{ec.pair}
		rr.TLSProvider = ""
	}
}

func (s *Service) externalConfig(app *models.Application) (base, provider string) {
	if s.external == nil {
		return "", ""
	}
	base, provider = s.external.ExternalAccess(app.ClusterID)
	return sanitizeBase(base), provider
}

// ExternalPort is one exposed container port and its generated public URL.
type ExternalPort struct {
	Port int    `json:"port"`
	Host string `json:"host"`
	URL  string `json:"url"`
}

// ExternalAccess is an app's external-access state for the UI.
type ExternalAccess struct {
	Enabled    bool           `json:"enabled"` // the app's cluster has an external domain
	BaseDomain string         `json:"base_domain"`
	Label      string         `json:"label"`
	Ports      []ExternalPort `json:"ports"` // currently exposed ports
}

func (s *Service) GetExternalAccess(workspaceID, appID uint) (*ExternalAccess, error) {
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, ErrAppRequired
	}
	base, _ := s.externalConfig(app)
	// Non-nil so the JSON is [] (not null) when no ports are exposed.
	out := &ExternalAccess{Enabled: base != "", BaseDomain: base, Label: app.ExternalLabel, Ports: []ExternalPort{}}
	if out.Label == "" {
		out.Label = s.defaultExternalLabel(app) // preview (not yet persisted)
	}
	routes, err := s.routes.ListByApp(appID)
	if err != nil {
		return nil, err
	}
	for i := range routes {
		rt := &routes[i]
		if !rt.Generated {
			continue
		}
		host := ""
		if len(rt.Hosts) > 0 {
			host = rt.Hosts[0]
		}
		out.Ports = append(out.Ports, ExternalPort{Port: rt.TargetPort, Host: host, URL: "https://" + host})
	}
	sort.Slice(out.Ports, func(i, j int) bool { return out.Ports[i].Port < out.Ports[j].Port })
	return out, nil
}

// SetExternalAccess reconciles generated routes per selected port; the primary port gets the bare label host.
func (s *Service) SetExternalAccess(ctx context.Context, workspaceID, appID uint, ports []int) (*ExternalAccess, error) {
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, ErrAppRequired
	}
	want := map[int]bool{}
	for _, p := range ports {
		if p > 0 {
			want[p] = true
		}
	}
	base, provider := s.externalConfig(app)
	// Disabling must always proceed so generated routes can be cleaned up even after the base domain is cleared.
	if len(want) > 0 {
		if base == "" {
			return nil, ErrExternalAccessDisabled
		}
		// Assign the stable subdomain label once, so the URL survives renames.
		if strings.TrimSpace(app.ExternalLabel) == "" {
			app.ExternalLabel = s.defaultExternalLabel(app)
			if err := s.apps.Update(app); err != nil {
				return nil, err
			}
		}
	}
	primary := primaryPort(app.Port, want)

	existing := map[int]*models.Route{}
	all, err := s.routes.ListByApp(appID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Generated {
			existing[all[i].TargetPort] = &all[i]
		}
	}

	// Upsert a generated route per selected port.
	for p := range want {
		host := app.ExternalLabel + "." + base
		if p != primary {
			host = fmt.Sprintf("%s-%d.%s", app.ExternalLabel, p, base)
		}
		if rt, ok := existing[p]; ok {
			rt.Hosts = []string{host}
			rt.TLSMode = models.RouteTLSACME
			rt.TLSProvider = provider
			rt.Generated = true
			rt.Enabled = true
			if err := s.routes.Update(rt); err != nil {
				return nil, err
			}
			continue
		}
		rt := &models.Route{
			WorkspaceID: workspaceID, ApplicationID: appID,
			Name: fmt.Sprintf("mb-ext-%d-%d", appID, p), Path: "/",
			Hosts: []string{host}, TargetPort: p,
			TLSMode: models.RouteTLSACME, TLSProvider: provider,
			Generated: true, Enabled: true,
		}
		if err := s.routes.Create(rt); err != nil {
			return nil, err
		}
	}
	// Remove generated routes for ports no longer selected.
	for p, rt := range existing {
		if !want[p] {
			_ = s.routes.Delete(rt.ID)
		}
	}
	_ = s.SyncRoute(ctx, appID)
	return s.GetExternalAccess(workspaceID, appID)
}

// RehostCluster moves a cluster's generated URLs onto its current external domain, or removes them if it has none.
func (s *Service) RehostCluster(ctx context.Context, clusterID uint) {
	apps, err := s.apps.ListByCluster(clusterID)
	if err != nil {
		logger.Error("re-host generated URLs: listing the cluster's apps failed", "cluster", clusterID, "error", err)
		return
	}
	for i := range apps {
		app := &apps[i]
		routes, err := s.routes.ListByApp(app.ID)
		if err != nil {
			logger.Warn("re-host generated URLs: listing routes failed", "app", app.ID, "error", err)
			continue
		}
		var ports []int
		for _, rt := range routes {
			if rt.Generated {
				ports = append(ports, rt.TargetPort)
			}
		}
		if len(ports) == 0 {
			continue
		}
		if base, _ := s.externalConfig(app); base == "" {
			ports = nil
		}
		if _, err := s.SetExternalAccess(ctx, app.WorkspaceID, app.ID, ports); err != nil {
			logger.Warn("re-host generated URLs failed", "app", app.ID, "error", err)
		}
	}
}

func (s *Service) defaultExternalLabel(app *models.Application) string {
	base := slug.Make(app.Name, "app")
	if base == "" {
		base = "app"
	}
	token := aliasToken(app.Alias)
	if token == "" {
		token = slug.Token(6)
	}
	return base + "-" + token
}

func aliasToken(alias string) string {
	parts := strings.Split(alias, "-")
	if len(parts) >= 4 && parts[0] == "mb" && parts[1] == "app" {
		return parts[2]
	}
	return ""
}

func primaryPort(appPort int, want map[int]bool) int {
	if want[appPort] {
		return appPort
	}
	primary := 0
	for p := range want {
		if primary == 0 || p < primary {
			primary = p
		}
	}
	return primary
}

func sanitizeBase(domain string) string {
	d := strings.TrimSpace(domain)
	d = strings.TrimPrefix(d, "*.")
	d = strings.TrimPrefix(d, ".")
	return strings.ToLower(d)
}
