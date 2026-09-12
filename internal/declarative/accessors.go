// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import "gopkg.in/yaml.v3"

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
