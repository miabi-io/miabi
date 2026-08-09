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

	// Configs: unique names, and the same key/size/mode rules the declarative
	// layer applies, so a template cannot pass catalog lint and fail at install.
	cfgNames := map[string]bool{}
	for _, c := range m.Configs {
		if !slugRe.MatchString(c.Name) {
			return fmt.Errorf("config name %q must match %s", c.Name, slugRe)
		}
		if cfgNames[c.Name] {
			return fmt.Errorf("duplicate config name %q", c.Name)
		}
		cfgNames[c.Name] = true
		if err := validateConfigFiles(c); err != nil {
			return err
		}
	}

	if len(m.Applications) > 0 {
		if err := m.validateApplications(volNames, cfgNames); err != nil {
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

func (m *Manifest) validateApplications(volNames, cfgNames map[string]bool) error {
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
			switch {
			case mt.Volume == "" && mt.Config == "":
				return fmt.Errorf("application %q: mount must set exactly one of volume or config", a.Name)
			case mt.Volume != "" && mt.Config != "":
				return fmt.Errorf("application %q: mount sets both volume %q and config %q", a.Name, mt.Volume, mt.Config)
			}
			if mt.Volume != "" && !volNames[mt.Volume] {
				return fmt.Errorf("application %q: mount references unknown volume %q", a.Name, mt.Volume)
			}
			if mt.Config != "" && !cfgNames[mt.Config] {
				return fmt.Errorf("application %q: mount references unknown config %q", a.Name, mt.Config)
			}
			if mt.Config == "" && (mt.Key != "" || mt.Mode != "") {
				return fmt.Errorf("application %q: mount key/mode are only valid with a config", a.Name)
			}
			if mt.Key != "" {
				if err := validateConfigKey(mt.Key); err != nil {
					return fmt.Errorf("application %q: mount key: %w", a.Name, err)
				}
			}
			if mt.Mode != "" {
				if err := validateFileMode(mt.Mode); err != nil {
					return fmt.Errorf("application %q: mount mode: %w", a.Name, err)
				}
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

// Config limits mirror internal/declarative. The per-file cap is what matters
// against Docker's 500 KB config-object limit, since projection is per file.
const (
	MaxConfigFileBytes  = 256 * 1024
	MaxConfigTotalBytes = 512 * 1024
)

var configKeyRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*)?(/[A-Za-z0-9._-]+)*$`)

func validateConfigFiles(c Config) error {
	if len(c.Files) == 0 {
		return fmt.Errorf("config %q: files must declare at least one entry", c.Name)
	}
	total := 0
	for k, v := range c.Files {
		if err := validateConfigKey(k); err != nil {
			return fmt.Errorf("config %q: %w", c.Name, err)
		}
		if len(v) > MaxConfigFileBytes {
			return fmt.Errorf("config %q: file %q is %d bytes, over the %d-byte limit", c.Name, k, len(v), MaxConfigFileBytes)
		}
		total += len(v)
	}
	if total > MaxConfigTotalBytes {
		return fmt.Errorf("config %q: total content is %d bytes, over the %d-byte limit", c.Name, total, MaxConfigTotalBytes)
	}
	if err := validateFileMode(c.Mode); err != nil {
		return fmt.Errorf("config %q: %w", c.Name, err)
	}
	if len(c.Delimiters) > 0 {
		if len(c.Delimiters) != 2 || c.Delimiters[0] == "" || c.Delimiters[1] == "" || c.Delimiters[0] == c.Delimiters[1] {
			return fmt.Errorf("config %q: delimiters must be exactly two distinct non-empty entries", c.Name)
		}
	}
	return nil
}

func validateConfigKey(key string) error {
	if !configKeyRe.MatchString(key) {
		return fmt.Errorf("file key %q must be a relative path matching %s", key, configKeyRe)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("file key %q must not contain path traversal", key)
		}
	}
	return nil
}

func validateFileMode(mode string) error {
	if mode == "" {
		return nil
	}
	if len(mode) < 3 || len(mode) > 4 {
		return fmt.Errorf("mode %q must be 3 or 4 octal digits", mode)
	}
	v, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return fmt.Errorf("mode %q is not octal", mode)
	}
	if v&0o7000 != 0 {
		return fmt.Errorf("mode %q must not set the setuid, setgid or sticky bit", mode)
	}
	return nil
}
