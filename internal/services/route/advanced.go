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
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Errors a rejected advanced config returns.
var (
	// Hosts, path and priority are the tenant isolation boundary: a route naming its own hosts can name another's.
	ErrAdvancedReservedKey = errors.New("advanced config may not set this field")
	ErrAdvancedUnknownKey  = errors.New("advanced config has an unsupported field")
)

var reservedAdvancedKeys = map[string]string{
	"hosts":       "set the route's hostnames on the route itself, so they are checked against your verified domains",
	"path":        "set the route's path on the route itself",
	"priority":    "route ordering is decided by Miabi, not by the route",
	"name":        "the route's name is derived from the workspace and route name",
	"enabled":     "enable or disable the route on the route itself",
	"disabled":    "enable or disable the route on the route itself",
	"target":      "the backend is taken from the route's application",
	"destination": "the backend is taken from the route's application",
	"backends":    "the backends are taken from the route's application",
}

type advancedRoute struct {
	Rewrite          string            `yaml:"rewrite"`
	Methods          []string          `yaml:"methods"`
	Middlewares      []string          `yaml:"middlewares"`
	HealthCheck      map[string]any    `yaml:"healthCheck"`
	Cors             map[string]any    `yaml:"cors"`
	ErrorInterceptor map[string]any    `yaml:"errorInterceptor"`
	DisableMetrics   bool              `yaml:"disableMetrics"`
	Security         *advancedSecurity `yaml:"security"`
	// Deprecated Goma spellings, still accepted: each is scoped to the route's own backend.
	BlockCommonExploits   bool `yaml:"blockCommonExploits"`
	DisableHostForwarding bool `yaml:"disableHostForwarding"`
	InsecureSkipVerify    bool `yaml:"insecureSkipVerify"`
}

// tls is omitted: its rootCAs/clientCert/clientKey are paths the gateway reads, so a tenant could point it anywhere.
type advancedSecurity struct {
	ForwardHostHeaders      *bool `yaml:"forwardHostHeaders"`
	EnableExploitProtection *bool `yaml:"enableExploitProtection"`
}

func validateAdvanced(cfg string) error {
	if strings.TrimSpace(cfg) == "" {
		return nil
	}
	// Decode loosely first to name the offending key, before the strict pass reports everything as "unknown field".
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(cfg), &raw); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidYAML, err)
	}
	for key := range raw {
		lower := strings.ToLower(strings.TrimSpace(key))
		// TLS keeps its own message: it predates this allow-list and says where to set TLS instead.
		if lower == "tls" {
			return ErrAdvancedTLSCert
		}
		if why, reserved := reservedAdvancedKeys[lower]; reserved {
			return fmt.Errorf("%w: %q — %s", ErrAdvancedReservedKey, key, why)
		}
	}

	dec := yaml.NewDecoder(bytes.NewReader([]byte(cfg)))
	dec.KnownFields(true)
	var out advancedRoute
	if err := dec.Decode(&out); err != nil {
		return fmt.Errorf("%w: %v", ErrAdvancedUnknownKey, err)
	}
	return nil
}
