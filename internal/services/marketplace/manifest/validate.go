// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	slugRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	validEngines = map[string]bool{"postgres": true, "mysql": true, "mariadb": true, "redis": true, "mongodb": true, "libsql": true}
)

// engineSupportsLogical reports whether an engine can host per-app logical
// databases (mirrors models.DatabaseInstance.SupportsLogicalDatabases). Redis
// cannot, so a Redis dependency is always satisfied by a dedicated instance.
func engineSupportsLogical(engine string) bool {
	return engine == "postgres" || engine == "mysql" || engine == "mariadb" || engine == "mongodb"
}

// Validate enforces the schema's semantic rules. It is intentionally strict:
// templates are untrusted input.
func (m *Manifest) Validate() error {
	if m.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q, got %q", APIVersion, m.APIVersion)
	}
	if m.Kind != KindValue {
		return fmt.Errorf("kind must be %q, got %q", KindValue, m.Kind)
	}
	if !slugRe.MatchString(m.Metadata.Name) {
		return fmt.Errorf("metadata.name %q must match %s", m.Metadata.Name, slugRe)
	}
	if strings.TrimSpace(m.Metadata.DisplayName) == "" {
		return fmt.Errorf("metadata.displayName is required")
	}
	if strings.TrimSpace(m.Metadata.Version) == "" {
		return fmt.Errorf("metadata.version is required")
	}
	if len(m.Applications) == 0 && len(m.Databases) == 0 {
		return fmt.Errorf("template must declare at least one application or database")
	}
	if a := m.Metadata.Author; a != nil {
		if strings.TrimSpace(a.Name) == "" {
			return fmt.Errorf("metadata.author.name is required when an author is given")
		}
		if a.Email != "" && !strings.Contains(a.Email, "@") {
			return fmt.Errorf("metadata.author.email %q is not a valid email", a.Email)
		}
		if a.Website != "" && !strings.HasPrefix(a.Website, "http://") && !strings.HasPrefix(a.Website, "https://") {
			return fmt.Errorf("metadata.author.website %q must start with http:// or https://", a.Website)
		}
	}

	// Inputs: unique keys.
	seenInput := map[string]bool{}
	for _, in := range m.Inputs {
		if in.Key == "" {
			return fmt.Errorf("input key is required")
		}
		if seenInput[in.Key] {
			return fmt.Errorf("duplicate input key %q", in.Key)
		}
		seenInput[in.Key] = true
		if in.Pattern != "" {
			if _, err := regexp.Compile(in.Pattern); err != nil {
				return fmt.Errorf("input %q: invalid pattern: %w", in.Key, err)
			}
		}
		if in.Length < 0 {
			return fmt.Errorf("input %q: length cannot be negative", in.Key)
		}
	}

	// Databases: unique names, known engine, valid placement, redis not shared.
	dbNames := map[string]bool{}
	for _, d := range m.Databases {
		if d.Name == "" {
			return fmt.Errorf("database name is required")
		}
		if dbNames[d.Name] {
			return fmt.Errorf("duplicate database name %q", d.Name)
		}
		dbNames[d.Name] = true
		if !validEngines[d.Engine] {
			return fmt.Errorf("database %q: unsupported engine %q", d.Name, d.Engine)
		}
		switch d.Placement {
		case PlacementAuto, PlacementDedicated, PlacementShared:
		default:
			return fmt.Errorf("database %q: invalid placement %q", d.Name, d.Placement)
		}
		if !engineSupportsLogical(d.Engine) && d.Placement == PlacementShared {
			return fmt.Errorf("database %q: engine %q has no logical databases; placement cannot be 'shared'", d.Name, d.Engine)
		}
	}

	// Volumes: unique names.
	volNames := map[string]bool{}
	for _, v := range m.Volumes {
		if v.Name == "" {
			return fmt.Errorf("volume name is required")
		}
		if volNames[v.Name] {
			return fmt.Errorf("duplicate volume name %q", v.Name)
		}
		volNames[v.Name] = true
	}

	if len(m.Applications) > 0 {
		if err := m.validateApplications(volNames); err != nil {
			return err
		}
	}

	// Stack: a stack groups applications, so a database-only template cannot
	// declare one; secretEnv keys must be present in the shared stack env.
	if m.Stack != nil {
		if len(m.Applications) == 0 {
			return fmt.Errorf("stack: a template with no applications cannot declare a stack")
		}
		for _, k := range m.Stack.SecretEnv {
			if _, ok := m.Stack.Env[k]; !ok {
				return fmt.Errorf("stack: secretEnv %q is not declared in stack env", k)
			}
		}
	}
	return nil
}

func (m *Manifest) validateApplications(volNames map[string]bool) error {
	appNames := map[string]bool{}
	primaries := 0
	for _, a := range m.Applications {
		if !slugRe.MatchString(a.Name) {
			return fmt.Errorf("application name %q must match %s", a.Name, slugRe)
		}
		if appNames[a.Name] {
			return fmt.Errorf("duplicate application name %q", a.Name)
		}
		appNames[a.Name] = true
		if a.Primary {
			primaries++
		}
		if strings.TrimSpace(a.Image) == "" {
			return fmt.Errorf("application %q: image is required", a.Name)
		}
		for _, p := range a.Ports {
			if p.Container <= 0 || p.Container > 65535 {
				return fmt.Errorf("application %q: invalid container port %d", a.Name, p.Container)
			}
			if p.Scheme != "http" && p.Scheme != "https" {
				return fmt.Errorf("application %q: port %d scheme must be http or https", a.Name, p.Container)
			}
		}
		for _, mt := range a.Mounts {
			if !volNames[mt.Volume] {
				return fmt.Errorf("application %q: mount references unknown volume %q", a.Name, mt.Volume)
			}
			if !strings.HasPrefix(mt.Path, "/") {
				return fmt.Errorf("application %q: mount path %q must be absolute", a.Name, mt.Path)
			}
		}
		// secretEnv keys must exist in env.
		for _, k := range a.SecretEnv {
			if _, ok := a.Env[k]; !ok {
				return fmt.Errorf("application %q: secretEnv %q is not declared in env", a.Name, k)
			}
		}
		if a.Resources != nil {
			if _, err := a.Resources.MemoryBytes(); err != nil {
				return fmt.Errorf("application %q: %w", a.Name, err)
			}
			if _, err := a.Resources.NanoCPUs(); err != nil {
				return fmt.Errorf("application %q: %w", a.Name, err)
			}
		}
	}
	if primaries > 1 {
		return fmt.Errorf("only one application may be marked primary (found %d)", primaries)
	}
	return nil
}

// MemoryBytes parses the memory cap (e.g. "512Mi", "1Gi", "0" or "") into bytes.
// Empty or "0" means unlimited (0).
func (r *Resources) MemoryBytes() (int64, error) {
	s := strings.TrimSpace(r.Memory)
	if s == "" || s == "0" {
		return 0, nil
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "Gi"):
		mult, s = 1<<30, strings.TrimSuffix(s, "Gi")
	case strings.HasSuffix(s, "Mi"):
		mult, s = 1<<20, strings.TrimSuffix(s, "Mi")
	case strings.HasSuffix(s, "Ki"):
		mult, s = 1<<10, strings.TrimSuffix(s, "Ki")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid memory %q", r.Memory)
	}
	return int64(n * float64(mult)), nil
}

// NanoCPUs parses the CPU cap (a core fraction, e.g. "0.5", "2") into nano-CPUs
// (1 core = 1e9). Empty or "0" means unlimited (0).
func (r *Resources) NanoCPUs() (int64, error) {
	s := strings.TrimSpace(r.CPU)
	if s == "" || s == "0" {
		return 0, nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid cpu %q", r.CPU)
	}
	return int64(n * 1e9), nil
}
