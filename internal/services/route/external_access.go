// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	"github.com/miabi-io/miabi/internal/slug"
)

// ErrExternalAccessDisabled is returned when one-click external access is used
// in a cluster that has no external domain.
var ErrExternalAccessDisabled = errors.New("external access is off in this app's location (a platform admin sets the cluster's external domain)")

// ExternalDomains resolves the wildcard domain and certManager provider for generated routes in a cluster.
// Implemented by services/cluster.
type ExternalDomains interface {
	ExternalAccess(clusterID uint) (domain, provider string)
}

// SetExternalDomains wires per-cluster external access; unset, external access is off everywhere.
func (s *Service) SetExternalDomains(d ExternalDomains) { s.external = d }

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

// GetExternalAccess reports an app's external-access state: whether its cluster
// has an external domain, its subdomain label, and the ports currently exposed
// (derived from the app's generated routes).
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

// SetExternalAccess reconciles the app's exposed ports: it generates a managed Route per selected port
// (host `<label>[-<port>].<base>` under the app's cluster domain, HTTPS via the cluster's certManager provider) and
// removes generated routes for ports no longer selected. The primary port gets the bare `<label>.<base>` host.
// Idempotent.
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
	// Exposing requires a base domain; disabling (no ports) must always proceed so the generated
	// routes can be cleaned up even if the base domain was later cleared.
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
	// SyncRoute re-renders the workspace file from the current DB state, so deleted
	// generated routes drop out and newly-created ones appear together.
	_ = s.SyncRoute(ctx, appID)
	return s.GetExternalAccess(workspaceID, appID)
}

// RehostCluster moves the generated URLs of every app in a cluster onto the cluster's current external domain and
// certificate provider, or removes them when the cluster no longer has a domain. Other routes are left alone.
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

// defaultExternalLabel derives a stable, DNS-safe subdomain label for an app:
// `<slug>-<alias-token>` (the alias token is stable and random), e.g.
// "blog-eqi3tlf2".
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

// primaryPort picks the bare-host port: the app's declared port when it is in the
// selected set, else the lowest selected port (0 when none).
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

// sanitizeBase normalizes a configured base domain: trims spaces and a leading
// "*." / "." so "*.apps.example.com" and "apps.example.com" are equivalent.
func sanitizeBase(domain string) string {
	d := strings.TrimSpace(domain)
	d = strings.TrimPrefix(d, "*.")
	d = strings.TrimPrefix(d, ".")
	return strings.ToLower(d)
}
