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

package manifest

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// RouteSpec is best-effort: a non-public host or one under no workspace domain is reported and skipped.
type RouteSpec struct {
	// App names the application the route serves; it must declare a port.
	App string `yaml:"app" json:"app"`
	// Host renders to a URL or a bare hostname; only the hostname is kept. It must come from an input.
	Host string `yaml:"host" json:"host"`
	// Port is the container port to route to; empty means the application's first port.
	Port int `yaml:"port,omitempty" json:"port,omitempty"`
}

func (m *Manifest) validateRoutes() error {
	apps := map[string]AppSpec{}
	for _, a := range m.Applications {
		apps[a.Name] = a
	}
	seen := map[string]bool{}
	for i, r := range m.Routes {
		a, ok := apps[r.App]
		if !ok {
			return fmt.Errorf("routes[%d]: unknown application %q", i, r.App)
		}
		if len(a.Ports) == 0 {
			return fmt.Errorf("routes[%d]: application %q declares no port to route to", i, r.App)
		}
		if r.Port != 0 && !hasPort(a, r.Port) {
			return fmt.Errorf("routes[%d]: application %q does not declare port %d", i, r.App, r.Port)
		}
		// A literal hostname would let a template claim a name the installer never chose.
		if !strings.Contains(r.Host, ".inputs.") {
			return fmt.Errorf("routes[%d]: host must be taken from an input, e.g. \"{{ .inputs.public_url }}\"", i)
		}
		key := r.App + "|" + r.Host
		if seen[key] {
			return fmt.Errorf("routes[%d]: duplicate route for %q", i, r.App)
		}
		seen[key] = true
	}
	return nil
}

func hasPort(a AppSpec, port int) bool {
	for _, p := range a.Ports {
		if p.Container == port {
			return true
		}
	}
	return false
}

// RouteHost returns "" for empty, localhost, IP addresses and single labels.
func RouteHost(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return ""
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if h == "" || h == "localhost" || net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return ""
	}
	return h
}
