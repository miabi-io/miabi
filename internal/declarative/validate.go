// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// hostnameRe matches a DNS hostname (dotted labels); used for Domain names,
	// whose metadata.name is a real FQDN rather than a slug.
	hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	// labelKeyRe / labelValRe constrain label and annotation keys/values, mirroring
	// the Kubernetes rules: a key is an optional DNS-subdomain prefix ("acme.io/")
	// followed by a name segment of alphanumerics plus -_. (max 63 chars); a value
	// is alphanumerics plus -_. (max 63 chars) or empty. Annotation values are
	// exempt — they hold arbitrary descriptive text.
	labelKeyRe     = regexp.MustCompile(`^([a-z0-9]([-a-z0-9.]*[a-z0-9])?/)?[a-zA-Z0-9]([-a-zA-Z0-9_.]*[a-zA-Z0-9])?$`)
	labelValRe     = regexp.MustCompile(`^[a-zA-Z0-9]([-a-zA-Z0-9_.]*[a-zA-Z0-9])?$`)
	validEngines   = map[string]bool{"postgres": true, "mysql": true, "mariadb": true, "redis": true}
	validTLS       = map[string]bool{"acme": true, "custom": true, "off": true}
	validDomainTLS = map[string]bool{"acme": true, "custom": true}
	placements     = map[string]bool{"auto": true, "dedicated": true, "shared": true}
)

func engineSupportsLogical(engine string) bool {
	return engine == "postgres" || engine == "mysql" || engine == "mariadb"
}

// normalize fills per-kind defaults so downstream code never special-cases
// empties.
func (r *Resource) normalize() {
	switch {
	case r.Application != nil:
		for i := range r.Application.Ports {
			if r.Application.Ports[i].Scheme == "" {
				r.Application.Ports[i].Scheme = "http"
			}
			if r.Application.Ports[i].Protocol == "" {
				r.Application.Ports[i].Protocol = "tcp"
			}
			// A requested host port implies the port is published.
			if r.Application.Ports[i].HostPort > 0 {
				r.Application.Ports[i].Publish = true
			}
		}
	case r.Database != nil:
		if r.Database.Placement == "" {
			r.Database.Placement = "auto"
		}
	case r.Route != nil:
		if r.Route.TLS == "" {
			r.Route.TLS = "acme"
		}
		if r.Route.Path == "" {
			r.Route.Path = "/"
		}
	case r.Domain != nil:
		if r.Domain.TLS == "" {
			r.Domain.TLS = "acme"
		}
	}
}

// validate enforces a single resource's semantic rules. Manifests are untrusted
// input, so validation is strict.
func (r *Resource) validate() error {
	// A Domain's name is a real FQDN (dotted); every other kind uses a slug.
	if r.Kind == KindDomain {
		if !hostnameRe.MatchString(r.Metadata.Name) {
			return fmt.Errorf("domain: metadata.name %q must be a valid hostname", r.Metadata.Name)
		}
	} else if !nameRe.MatchString(r.Metadata.Name) {
		return fmt.Errorf("%s: metadata.name %q must match %s", r.Kind, r.Metadata.Name, nameRe)
	}
	if err := validateMeta(r.Kind, r.Metadata); err != nil {
		return err
	}
	switch r.Kind {
	case KindApplication:
		return r.validateApplication()
	case KindDatabase:
		return r.validateDatabase()
	case KindRoute:
		return r.validateRoute()
	case KindDomain:
		return r.validateDomain()
	case KindVolume, KindStack, KindSecret, KindProject:
		return nil
	default:
		return fmt.Errorf("unknown kind %q", r.Kind)
	}
}

// validateMeta enforces the key/value rules for the two free-form metadata maps.
// Keys are constrained for both maps; label values are constrained too, while
// annotation values are left arbitrary (they exist precisely to hold free text).
const maxMetaSegment = 63

func validateMeta(kind Kind, m Meta) error {
	for k, v := range m.Labels {
		if err := validMetaKey("label", kind, k); err != nil {
			return err
		}
		if len(v) > maxMetaSegment || (v != "" && !labelValRe.MatchString(v)) {
			return fmt.Errorf("%s %q: label %q has invalid value %q (alphanumerics, '-', '_', '.', max %d chars)", kind, m.Name, k, v, maxMetaSegment)
		}
	}
	for k := range m.Annotations {
		if err := validMetaKey("annotation", kind, k); err != nil {
			return err
		}
	}
	return nil
}

func validMetaKey(what string, kind Kind, key string) error {
	// Validate the name segment length (after any "prefix/") independently of the
	// optional DNS-subdomain prefix, matching Kubernetes' 63-char name limit.
	name := key
	if i := strings.LastIndex(key, "/"); i >= 0 {
		name = key[i+1:]
	}
	if key == "" || len(name) > maxMetaSegment || !labelKeyRe.MatchString(key) {
		return fmt.Errorf("%s: %s key %q is invalid (optional 'prefix/' then alphanumerics, '-', '_', '.', max %d chars)", kind, what, key, maxMetaSegment)
	}
	return nil
}

func (r *Resource) validateDomain() error {
	if r.Domain == nil {
		return nil // spec is optional; tls defaults to acme
	}
	if !validDomainTLS[r.Domain.TLS] {
		return fmt.Errorf("domain %q: tls must be acme or custom", r.Metadata.Name)
	}
	return nil
}

func (r *Resource) validateApplication() error {
	a := r.Application
	if a == nil {
		return fmt.Errorf("application %q: spec is required", r.Metadata.Name)
	}
	if strings.TrimSpace(a.Image) == "" {
		return fmt.Errorf("application %q: image is required", r.Metadata.Name)
	}
	if a.Digest != "" && !strings.HasPrefix(a.Digest, "sha256:") {
		return fmt.Errorf("application %q: digest must be a sha256: reference", r.Metadata.Name)
	}
	for _, p := range a.Ports {
		if p.Container <= 0 || p.Container > 65535 {
			return fmt.Errorf("application %q: invalid container port %d", r.Metadata.Name, p.Container)
		}
		if p.Scheme != "http" && p.Scheme != "https" {
			return fmt.Errorf("application %q: port %d scheme must be http or https", r.Metadata.Name, p.Container)
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			return fmt.Errorf("application %q: port %d protocol must be tcp or udp", r.Metadata.Name, p.Container)
		}
		if p.HostPort < 0 || p.HostPort > 65535 {
			return fmt.Errorf("application %q: port %d hostPort %d out of range", r.Metadata.Name, p.Container, p.HostPort)
		}
		if p.ExternalAccess && p.Scheme != "http" && p.Scheme != "https" {
			return fmt.Errorf("application %q: port %d externalAccess needs an http/https scheme", r.Metadata.Name, p.Container)
		}
	}
	if a.ExternalLabel != "" && !nameRe.MatchString(a.ExternalLabel) {
		return fmt.Errorf("application %q: externalLabel %q must be a DNS label", r.Metadata.Name, a.ExternalLabel)
	}
	for _, mt := range a.Mounts {
		if mt.Volume == "" {
			return fmt.Errorf("application %q: mount is missing a volume", r.Metadata.Name)
		}
		if !strings.HasPrefix(mt.Path, "/") {
			return fmt.Errorf("application %q: mount path %q must be absolute", r.Metadata.Name, mt.Path)
		}
	}
	for _, k := range a.SecretEnv {
		if _, ok := a.Env[k]; !ok {
			return fmt.Errorf("application %q: secretEnv %q is not declared in env", r.Metadata.Name, k)
		}
	}
	if a.Resources != nil {
		if _, err := a.Resources.MemoryBytes(); err != nil {
			return fmt.Errorf("application %q: %w", r.Metadata.Name, err)
		}
		if _, err := a.Resources.NanoCPUs(); err != nil {
			return fmt.Errorf("application %q: %w", r.Metadata.Name, err)
		}
		if a.Resources.GPU < 0 || a.Resources.GPU > 64 {
			return fmt.Errorf("application %q: invalid gpu %d (must be 0-64)", r.Metadata.Name, a.Resources.GPU)
		}
	}
	return nil
}

func (r *Resource) validateDatabase() error {
	d := r.Database
	if d == nil {
		return fmt.Errorf("database %q: spec is required", r.Metadata.Name)
	}
	if !validEngines[d.Engine] {
		return fmt.Errorf("database %q: unsupported engine %q", r.Metadata.Name, d.Engine)
	}
	if !placements[d.Placement] {
		return fmt.Errorf("database %q: invalid placement %q", r.Metadata.Name, d.Placement)
	}
	if !engineSupportsLogical(d.Engine) && d.Placement == "shared" {
		return fmt.Errorf("database %q: engine %q has no logical databases; placement cannot be 'shared'", r.Metadata.Name, d.Engine)
	}
	return nil
}

// hasNonEmpty reports whether s contains at least one non-blank entry.
func hasNonEmpty(s []string) bool {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func (r *Resource) validateRoute() error {
	rt := r.Route
	if rt == nil {
		return fmt.Errorf("route %q: spec is required", r.Metadata.Name)
	}
	if !hasNonEmpty(rt.Hosts) {
		return fmt.Errorf("route %q: at least one host is required", r.Metadata.Name)
	}
	if strings.TrimSpace(rt.App) == "" {
		return fmt.Errorf("route %q: app target is required", r.Metadata.Name)
	}
	if !validTLS[rt.TLS] {
		return fmt.Errorf("route %q: tls must be acme, custom or off", r.Metadata.Name)
	}
	return nil
}

// validateReferences checks cross-resource integrity once the whole set is
// known: mounts point at declared volumes, domains target declared apps, and an
// app's stack exists.
func (s *ResourceSet) validateReferences() error {
	for _, r := range s.list {
		switch {
		case r.Application != nil:
			for _, mt := range r.Application.Mounts {
				if !s.Has(KindVolume, mt.Volume) {
					return fmt.Errorf("application %q: mount references unknown volume %q", r.Metadata.Name, mt.Volume)
				}
			}
			if r.Application.Stack != "" && !s.Has(KindStack, r.Application.Stack) {
				return fmt.Errorf("application %q: references unknown stack %q", r.Metadata.Name, r.Application.Stack)
			}
		case r.Route != nil:
			if !s.Has(KindApplication, r.Route.App) {
				return fmt.Errorf("route %q: targets unknown application %q", r.Metadata.Name, r.Route.App)
			}
		}
	}
	return nil
}

// MemoryBytes parses the memory cap (e.g. "512Mi", "1Gi") into bytes. Empty or
// "0" means unlimited (0).
func (rs *ResourceSpec) MemoryBytes() (int64, error) {
	s := strings.TrimSpace(rs.Memory)
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
		return 0, fmt.Errorf("invalid memory %q", rs.Memory)
	}
	return int64(n * float64(mult)), nil
}

// NanoCPUs parses the CPU cap (a core fraction, e.g. "0.5", "2") into nano-CPUs
// (1 core = 1e9). Empty or "0" means unlimited (0).
func (rs *ResourceSpec) NanoCPUs() (int64, error) {
	s := strings.TrimSpace(rs.CPU)
	if s == "" || s == "0" {
		return 0, nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid cpu %q", rs.CPU)
	}
	return int64(n * 1e9), nil
}
