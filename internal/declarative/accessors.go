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

package declarative

import (
	"encoding/json"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Location is the location the app names, or "" when it names none.
func (a *ApplicationSpec) Location() string {
	if a == nil || a.Placement == nil {
		return ""
	}
	return a.Placement.Location
}

// Constraints are the app's Swarm placement constraints; nil when unstated, empty when cleared.
func (a *ApplicationSpec) Constraints() []string {
	if a == nil || a.Placement == nil {
		return nil
	}
	return a.Placement.Constraints
}

// Runtime is the runtime the app names, or "" when it names none.
func (a *ApplicationSpec) Runtime() string {
	if a == nil || a.Deployment == nil {
		return ""
	}
	return a.Deployment.Runtime
}

// Replicas is the replica count the app names, or 0 when it names none.
func (a *ApplicationSpec) Replicas() int {
	if a == nil || a.Deployment == nil {
		return 0
	}
	return a.Deployment.Replicas
}

// Strategy is the rollout strategy the app names, or "" when it names none.
func (a *ApplicationSpec) Strategy() string {
	if a == nil || a.Deployment == nil {
		return ""
	}
	return a.Deployment.Strategy
}

// Update is the service rollout tuning, or nil when unstated.
func (a *ApplicationSpec) Update() *UpdateSpec {
	if a == nil || a.Deployment == nil {
		return nil
	}
	return a.Deployment.Update
}

// RunAsUser is the account the container runs as, or "" for the image's own.
func (a *ApplicationSpec) RunAsUser() string {
	if a == nil || a.Security == nil {
		return ""
	}
	return a.Security.RunAsUser
}

// ReadOnlyRootFilesystem reports whether the container's root filesystem is read-only.
func (a *ApplicationSpec) ReadOnlyRootFilesystem() bool {
	return a != nil && a.Security != nil && a.Security.ReadOnlyRootFilesystem
}

// NoNewPrivileges reports whether the app asks for no-new-privileges.
func (a *ApplicationSpec) NoNewPrivileges() bool {
	return a != nil && a.Security != nil && a.Security.NoNewPrivileges != nil && *a.Security.NoNewPrivileges
}

// AddCapabilities are the capabilities the app is granted.
func (a *ApplicationSpec) AddCapabilities() []string {
	if a == nil || a.Security == nil || a.Security.Capabilities == nil {
		return nil
	}
	return a.Security.Capabilities.Add
}

// DropCapabilities are the capabilities the app drops.
func (a *ApplicationSpec) DropCapabilities() []string {
	if a == nil || a.Security == nil || a.Security.Capabilities == nil {
		return nil
	}
	return a.Security.Capabilities.Drop
}

// Devices are the host devices exposed to the app.
func (a *ApplicationSpec) Devices() []string {
	if a == nil || a.Security == nil {
		return nil
	}
	return a.Security.Devices
}

// Location is the location the stack names, or "".
func (s *StackSpec) Location() string {
	if s == nil || s.Placement == nil {
		return ""
	}
	return s.Placement.Location
}

// Location is the location the database names, or "".
func (d *DatabaseSpec) Location() string {
	if d == nil || d.Placement == nil {
		return ""
	}
	return d.Placement.Location
}

// Location is the location the volume names, or "".
func (v *VolumeSpec) Location() string {
	if v == nil || v.Placement == nil {
		return ""
	}
	return v.Placement.Location
}

// MemoryBytes parses the memory limit into bytes; empty or "0" is unlimited.
func (r *DatabaseResourcesSpec) MemoryBytes() (int64, error) {
	return (&ResourceSpec{Memory: r.Memory}).MemoryBytes()
}

// NanoCPUs parses the CPU limit into nano-CPUs; empty or "0" is unlimited.
func (r *DatabaseResourcesSpec) NanoCPUs() (int64, error) {
	return (&ResourceSpec{CPU: r.CPU}).NanoCPUs()
}

// UnmarshalYAML reads the block, or the bare string the field held before it became one.
func (p *DatabasePlacementSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		p.legacyInstance = n.Value
		return nil
	}
	type plain DatabasePlacementSpec
	return decodeSpec(n, (*plain)(p))
}

// UnmarshalYAML reads the block, or the bare acme|custom|off string the field held before it became one.
func (t *RouteTLSSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		t.legacyMode = n.Value
		return nil
	}
	type plain RouteTLSSpec
	return decodeSpec(n, (*plain)(t))
}

// TLSMode is acme, custom or off, read from the block or its old string form.
func (r *RouteSpec) TLSMode() string {
	if r.TLS != nil {
		if r.TLS.Enabled != nil && !*r.TLS.Enabled {
			return "off"
		}
		if r.TLS.legacyMode != "" {
			return r.TLS.legacyMode
		}
	}
	if r.TLSCertificate() != "" {
		return "custom"
	}
	return "acme"
}

// TLSCertificate is the imported certificate the route serves, or "".
func (r *RouteSpec) TLSCertificate() string {
	if r.TLS != nil && r.TLS.Certificate != "" {
		return r.TLS.Certificate
	}
	return r.DeprecatedCertificate
}

// TLSProvider is the gateway certificate provider for an ACME route, or "" for the default.
func (r *RouteSpec) TLSProvider() string {
	if r.TLS != nil && r.TLS.Provider != "" {
		return r.TLS.Provider
	}
	return r.DeprecatedTLSProvider
}

// IsZero lets omitempty drop an unset port.
func (p RoutePort) IsZero() bool { return p.Number == 0 && p.Name == "" }

func (p RoutePort) String() string {
	if p.Name != "" {
		return p.Name
	}
	return strconv.Itoa(p.Number)
}

func (p *RoutePort) set(v string) {
	// A quoted number is still a number, so "8080" never becomes a port name.
	if n, err := strconv.Atoi(v); err == nil {
		p.Number, p.Name = n, ""
		return
	}
	p.Number, p.Name = 0, v
}

func (p *RoutePort) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: port must be a number or a port name", n.Line)
	}
	p.set(n.Value)
	return nil
}

func (p RoutePort) MarshalYAML() (any, error) {
	if p.Name != "" {
		return p.Name, nil
	}
	return p.Number, nil
}

func (p *RoutePort) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case float64:
		p.Number, p.Name = int(x), ""
	case string:
		p.set(x)
	case nil:
		*p = RoutePort{}
	default:
		return fmt.Errorf("port must be a number or a port name")
	}
	return nil
}

func (p RoutePort) MarshalJSON() ([]byte, error) {
	if p.Name != "" {
		return json.Marshal(p.Name)
	}
	return json.Marshal(p.Number)
}

// RouteTLSOf builds the block a stored route exports as, from its acme|custom|off mode.
func RouteTLSOf(mode, provider, certificate string) *RouteTLSSpec {
	switch mode {
	case "off":
		off := false
		return &RouteTLSSpec{Enabled: &off}
	case "custom":
		if certificate == "" {
			// Keeps the live mode visible to the plan when the certificate was deleted.
			return &RouteTLSSpec{legacyMode: "custom"}
		}
		return &RouteTLSSpec{Certificate: certificate}
	}
	if provider != "" {
		return &RouteTLSSpec{Provider: provider}
	}
	return nil
}

// ValidPortName refuses a digit-only name, which a Route would read as a port number.
func ValidPortName(name string) bool {
	return len(name) <= 63 && portNameRe.MatchString(name) && !isDigits(name)
}
