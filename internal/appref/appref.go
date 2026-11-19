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

// Package appref resolves ${{ applications.<name>.<field> }} references to an application's public address.
package appref

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

const (
	FieldPublicURL   = "public_url"
	FieldPublicHost  = "public_host"
	FieldExternalURL = "external_url"
)

// Fields are the deploy-time fields; the internal ones (url, host, port) render when a manifest is applied.
var Fields = []string{FieldPublicURL, FieldPublicHost, FieldExternalURL}

func IsField(field string) bool {
	for _, f := range Fields {
		if f == field {
			return true
		}
	}
	return false
}

// The field is matched loosely so a typo is reported by name instead of passing through as literal text.
var refRe = regexp.MustCompile(`\$\{\{\s*(?:applications|apps)\.([A-Za-z0-9][A-Za-z0-9_-]*)\.([A-Za-z0-9_]+)\s*\}\}`)

type Ref struct {
	App   string
	Field string
}

// Token is the canonical stored form.
func Token(app, field string) string {
	return "${{ applications." + app + "." + field + " }}"
}

func HasRef(value string) bool { return refRe.MatchString(value) }

func Refs(value string) []Ref {
	var out []Ref
	for _, m := range refRe.FindAllStringSubmatch(value, -1) {
		out = append(out, Ref{App: strings.ToLower(m[1]), Field: m[2]})
	}
	return out
}

func References(value, app string) bool {
	app = strings.ToLower(app)
	for _, r := range Refs(value) {
		if r.App == app {
			return true
		}
	}
	return false
}

// ValidateFields rejects unknown fields without requiring the application to exist yet.
func ValidateFields(value string) error {
	for _, r := range Refs(value) {
		if !IsField(r.Field) {
			return fmt.Errorf("application reference %q has no field %q (use %s)", r.App, r.Field, strings.Join(Fields, ", "))
		}
	}
	return nil
}

// Address is an application's public address; empty strings mean it has none.
type Address struct {
	URL          string
	Host         string
	ExternalURL  string
	ExternalHost string
}

func (a Address) value(field string) string {
	switch field {
	case FieldPublicURL:
		return a.URL
	case FieldPublicHost:
		return a.Host
	case FieldExternalURL:
		return a.ExternalURL
	}
	return ""
}

// AddressOf prefers the oldest root custom route on the app's port, then the generated route on its primary port.
func AddressOf(app *models.Application, routes []models.Route) Address {
	var out Address
	sorted := append([]models.Route(nil), routes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	for i := range sorted {
		rt := &sorted[i]
		if rt.Generated || !rt.Enabled || len(rt.Hosts) == 0 || strings.TrimSpace(rt.Hosts[0]) == "" {
			continue
		}
		if rt.TargetPort != 0 && rt.TargetPort != app.Port {
			continue
		}
		if p := strings.TrimSpace(rt.Path); p != "" && p != "/" {
			continue
		}
		scheme := "https"
		if rt.TLSMode == models.RouteTLSNone {
			scheme = "http"
		}
		out.Host = strings.TrimSpace(rt.Hosts[0])
		out.URL = scheme + "://" + out.Host
		break
	}

	generated := map[int]string{}
	for i := range sorted {
		rt := &sorted[i]
		if !rt.Generated || !rt.Enabled || len(rt.Hosts) == 0 || strings.TrimSpace(rt.Hosts[0]) == "" {
			continue
		}
		if _, seen := generated[rt.TargetPort]; !seen {
			generated[rt.TargetPort] = strings.TrimSpace(rt.Hosts[0])
		}
	}
	if host, ok := generated[primaryGeneratedPort(app.Port, generated)]; ok {
		out.ExternalHost = host
		out.ExternalURL = "https://" + host
	}
	if out.URL == "" {
		out.URL, out.Host = out.ExternalURL, out.ExternalHost
	}
	return out
}

// Mirrors route.primaryPort, which gives the app's port the bare label host.
func primaryGeneratedPort(appPort int, ports map[int]string) int {
	if _, ok := ports[appPort]; ok {
		return appPort
	}
	primary := 0
	for p := range ports {
		if primary == 0 || p < primary {
			primary = p
		}
	}
	return primary
}

type AppFinder interface {
	FindByName(workspaceID uint, name string) (*models.Application, error)
}

type RouteLister interface {
	ListByApp(appID uint) ([]models.Route, error)
}

type Resolver struct {
	apps   AppFinder
	routes RouteLister
}

func NewResolver(apps AppFinder, routes RouteLister) *Resolver {
	return &Resolver{apps: apps, routes: routes}
}

var errUnavailable = errors.New("application references cannot be resolved in this process")

func (r *Resolver) Lookup(workspaceID uint, name string) (Address, error) {
	if r == nil || r.apps == nil || r.routes == nil {
		return Address{}, errUnavailable
	}
	app, err := r.apps.FindByName(workspaceID, strings.ToLower(name))
	if err != nil || app == nil {
		return Address{}, fmt.Errorf("application %q referenced in env does not exist in this workspace", name)
	}
	routes, err := r.routes.ListByApp(app.ID)
	if err != nil {
		return Address{}, fmt.Errorf("list routes of application %q: %w", name, err)
	}
	return AddressOf(app, routes), nil
}

// ResolveAll fails on any unresolvable reference, so a deploy never starts with a blank address.
func (r *Resolver) ResolveAll(workspaceID uint, env []string) ([]string, error) {
	hasRef := false
	for _, v := range env {
		if HasRef(v) {
			hasRef = true
			break
		}
	}
	if !hasRef {
		return env, nil
	}
	cache := map[string]Address{}
	var firstErr error
	out := make([]string, len(env))
	for i, v := range env {
		out[i] = refRe.ReplaceAllStringFunc(v, func(match string) string {
			if firstErr != nil {
				return match
			}
			m := refRe.FindStringSubmatch(match)
			name, field := strings.ToLower(m[1]), m[2]
			if !IsField(field) {
				firstErr = fmt.Errorf("application reference %q has no field %q (use %s)", name, field, strings.Join(Fields, ", "))
				return match
			}
			addr, ok := cache[name]
			if !ok {
				var err error
				if addr, err = r.Lookup(workspaceID, name); err != nil {
					firstErr = err
					return match
				}
				cache[name] = addr
			}
			val := addr.value(field)
			if val == "" {
				if field == FieldExternalURL {
					firstErr = fmt.Errorf("application %q has no external-access URL: turn on external access for its port", name)
				} else {
					firstErr = fmt.Errorf("application %q has no public URL: turn on external access or add a route on its port", name)
				}
				return match
			}
			return val
		})
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}
