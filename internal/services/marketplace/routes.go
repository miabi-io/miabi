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

package marketplace

import (
	"context"
	"errors"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/slug"
)

// RouteCreator creates the routes a template declares. Satisfied by *route.Service.
type RouteCreator interface {
	Create(ctx context.Context, workspaceID uint, in route.Input) (*models.Route, error)
	CoveringDomain(workspaceID uint, host string) (*models.Domain, error)
}

// SetRoutes wires route creation. Left nil, a template's routes are reported as skipped.
func (s *Service) SetRoutes(r RouteCreator) { s.routes = r }

// Goes through the route service for the same domain ownership checks; a failure is only a warning.
func (s *Service) createRoutes(ctx context.Context, workspaceID uint, m *manifest.Manifest, apps []*models.Application,
	r *manifest.Renderer, displayName string, report *reporter) (created []*models.Route, warnings []string) {
	warn := func(msg string) {
		warnings = append(warnings, msg)
		report.warn(msg)
	}
	byName := map[string]*models.Application{}
	specs := map[string]manifest.AppSpec{}
	for i, spec := range m.Applications {
		byName[spec.Name] = apps[i]
		specs[spec.Name] = spec
	}
	for i, rs := range m.Routes {
		app, spec := byName[rs.App], specs[rs.App]
		label := app.DisplayName
		if s.routes == nil {
			warn(fmt.Sprintf("No route for %s: routes are not available on this installation.", label))
			continue
		}
		raw, err := r.RenderString(fmt.Sprintf("routes[%d].host", i), rs.Host)
		if err != nil {
			warn(fmt.Sprintf("No route for %s: %v", label, err))
			continue
		}
		host := manifest.RouteHost(raw)
		switch {
		case raw == "":
			warn(fmt.Sprintf("No route for %s: no public URL was given.", label))
			continue
		case host == "":
			warn(fmt.Sprintf("No route for %s: %q is not a public hostname.", label, raw))
			continue
		}
		port := rs.Port
		if port == 0 {
			port = spec.Ports[0].Container
		}
		rt, err := s.createRoute(ctx, workspaceID, app, displayName, rs.App, host, port, m)
		if err != nil {
			warn(routeSkipMessage(host, label, err))
			continue
		}
		created = append(created, rt)
		if d, derr := s.routes.CoveringDomain(workspaceID, host); derr == nil && d != nil && !d.Verified {
			warn(fmt.Sprintf("Route for %s created; it goes live once %s is verified.", host, d.Name))
		}
	}
	return created, warnings
}

// createRoute names the route after the install and app, retrying with a suffix when the name is taken.
func (s *Service) createRoute(ctx context.Context, workspaceID uint, app *models.Application, displayName, appName, host string,
	port int, m *manifest.Manifest) (*models.Route, error) {
	base := slug.Make(displayName+"-"+appName, "route")
	in := route.Input{
		DisplayName:   host,
		ApplicationID: app.ID,
		Hosts:         []string{host},
		TargetPort:    port,
		Metadata: models.SetBuiltin(models.Metadata{},
			models.MetaManagedBy, models.ManagedByMarketplace,
			models.MetaTemplate, m.Metadata.Name,
			models.MetaTemplateVersion, m.Metadata.Version,
		),
	}
	for n := 1; ; n++ {
		in.Name = base
		if n > 1 {
			in.Name = fmt.Sprintf("%s-%d", base, n)
		}
		rt, err := s.routes.Create(ctx, workspaceID, in)
		if errors.Is(err, route.ErrNameTaken) && n < 10 {
			continue
		}
		return rt, err
	}
}

func routeSkipMessage(host, label string, err error) string {
	switch {
	case errors.Is(err, route.ErrDomainNotRegistered):
		return fmt.Sprintf("Route for %s skipped: its domain is not registered in this workspace. Add and verify the domain, then add a route to %s.", host, label)
	case errors.Is(err, route.ErrDomainBanned):
		return fmt.Sprintf("Route for %s skipped: its domain is banned by a platform administrator.", host)
	case errors.Is(err, route.ErrHostTaken):
		return fmt.Sprintf("Route for %s skipped: another route already uses this hostname.", host)
	default:
		return fmt.Sprintf("Route for %s skipped: %v", host, err)
	}
}
