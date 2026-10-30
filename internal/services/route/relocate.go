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

	"github.com/miabi-io/miabi/internal/models"
)

// GatewayServer returns the edge node whose gateway serves an app, or false when the control-plane gateway
// does. A move records it first, so the gateway the app leaves can be told to drop it.
func (s *Service) GatewayServer(app *models.Application) (uint, bool) { return s.gatewayServer(app) }

// ReloadGateway tells an edge gateway to pull its config now instead of on its next poll. Best-effort.
func (s *Service) ReloadGateway(ctx context.Context, serverID uint) {
	if s.reloader == nil || serverID == 0 {
		return
	}
	s.reloader.ReloadServers(ctx, []uint{serverID})
}

// DNSTarget is the public address an app's hosts should resolve to, for the report a move writes.
func (s *Service) DNSTarget(app *models.Application) models.PublicAddress { return s.dnsTarget(app) }

// RehostApp moves an app's generated URLs onto its current cluster's external domain, or removes them when
// that cluster has none. It is RehostCluster for one app, run after the app changes cluster.
func (s *Service) RehostApp(ctx context.Context, app *models.Application) ([]string, error) {
	routes, err := s.routes.ListByApp(app.ID)
	if err != nil {
		return nil, err
	}
	var ports []int
	for _, rt := range routes {
		if rt.Generated {
			ports = append(ports, rt.TargetPort)
		}
	}
	if len(ports) == 0 {
		return nil, nil
	}
	if base, _ := s.externalConfig(app); base == "" {
		ports = nil
	}
	ea, err := s.SetExternalAccess(ctx, app.WorkspaceID, app.ID, ports)
	if err != nil || ea == nil {
		return nil, err
	}
	urls := make([]string, 0, len(ea.Ports))
	for _, p := range ea.Ports {
		urls = append(urls, p.URL)
	}
	return urls, nil
}

// GeneratedURLs lists an app's generated URLs as they stand, for a plan to show what will change.
func (s *Service) GeneratedURLs(workspaceID, appID uint) []string {
	ea, err := s.GetExternalAccess(workspaceID, appID)
	if err != nil || ea == nil {
		return nil
	}
	urls := make([]string, 0, len(ea.Ports))
	for _, p := range ea.Ports {
		urls = append(urls, p.URL)
	}
	return urls
}

// CustomHosts lists the hosts of an app's own (not generated) routes: the names whose DNS must follow a move.
func (s *Service) CustomHosts(appID uint) []string {
	routes, err := s.routes.ListByApp(appID)
	if err != nil {
		return nil
	}
	var hosts []string
	for _, rt := range routes {
		if rt.Generated || !rt.Enabled {
			continue
		}
		hosts = append(hosts, rt.Hosts...)
	}
	return hosts
}
