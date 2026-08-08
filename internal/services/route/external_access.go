// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package route

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/slug"
)

// ErrExternalAccessDisabled is returned when one-click external access is used
// before an admin configures the wildcard base domain.
var ErrExternalAccessDisabled = errors.New("external access is not configured (an admin must set the base domain in platform settings)")

// ExternalConfig is the platform-level external-access config (from settings):
// the wildcard base domain and the certManager provider for generated routes.
type ExternalConfig struct {
	BaseDomain string
	Provider   string
}

// ExternalPort is one exposed container port and its generated public URL.
type ExternalPort struct {
	Port int    `json:"port"`
	Host string `json:"host"`
	URL  string `json:"url"`
}

// ExternalAccess is an app's external-access state for the UI.
type ExternalAccess struct {
	Enabled    bool           `json:"enabled"` // base domain configured platform-wide
	BaseDomain string         `json:"base_domain"`
	Label      string         `json:"label"`
	Ports      []ExternalPort `json:"ports"` // currently exposed ports
}

// GetExternalAccess reports an app's external-access state: whether the feature
// is enabled, its subdomain label, and the ports currently exposed (derived from
// the app's generated routes).
func (s *Service) GetExternalAccess(workspaceID, appID uint, cfg ExternalConfig) (*ExternalAccess, error) {
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, ErrAppRequired
	}
	base := sanitizeBase(cfg.BaseDomain)
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
// (host `<label>[-<port>].<base>`, HTTPS via the configured certManager provider) and removes generated
// routes for ports no longer selected. The primary port gets the bare `<label>.<base>` host. Idempotent.
func (s *Service) SetExternalAccess(ctx context.Context, workspaceID, appID uint, ports []int, cfg ExternalConfig) (*ExternalAccess, error) {
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
	base := sanitizeBase(cfg.BaseDomain)
	// Exposing requires a base domain and a reachable node; disabling (no ports)
	// must always proceed so the generated routes can be cleaned up even if the
	// base domain was later cleared or the node lost its address.
	if len(want) > 0 {
		if base == "" {
			return nil, ErrExternalAccessDisabled
		}
		if err := s.requireRoutableNode(app); err != nil {
			return nil, err
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
			rt.TLSProvider = cfg.Provider
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
			TLSMode: models.RouteTLSACME, TLSProvider: cfg.Provider,
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
	return s.GetExternalAccess(workspaceID, appID, cfg)
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
