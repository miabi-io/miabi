// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// ErrAdvancedReservedKey is returned when an advanced config sets a field Miabi owns. The fields
	// that decide WHICH requests reach a route — hosts, path, priority — are the whole isolation
	// boundary between tenants: a route that can name its own hosts can name somebody else's, and one
	// that can set its own path can outrank the console's on the platform domain.
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

// advancedSecurity omits `tls`: it carries rootCAs, clientCert and clientKey, which are paths the
// GATEWAY reads, so a tenant could otherwise point the gateway at files it should never read. The
// renderer sets the one field that matters (insecureSkipVerify for an https backend) itself.
type advancedSecurity struct {
	ForwardHostHeaders      *bool `yaml:"forwardHostHeaders"`
	EnableExploitProtection *bool `yaml:"enableExploitProtection"`
}

// validateAdvanced checks a hand-written advanced route config: every field must be one Miabi does
// not own, and the whole document must decode strictly.
func validateAdvanced(cfg string) error {
	if strings.TrimSpace(cfg) == "" {
		return nil
	}
	// Decode once loosely to name the offending key precisely, before the strict pass turns
	// everything into "unknown field".
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
