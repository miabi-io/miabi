// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// Config limits and defaults. The per-file cap is what matters against Docker's
// 500 KB config-object limit, since projection is always per file.
const (
	MaxConfigFileBytes  = 256 * 1024
	MaxConfigTotalBytes = 512 * 1024
	DefaultFileMode     = "0644"
	ReloadRestart       = "restart"
	ReloadNone          = "none"
)

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// configKeyRe matches a relative file key, optionally nested in subdirectories.
	configKeyRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*)?(/[A-Za-z0-9._-]+)*$`)
	// hostnameRe matches a DNS hostname (dotted labels); used for Domain names,
	// whose metadata.name is a real FQDN rather than a slug.
	hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	// labelKeyRe / labelValRe constrain label and annotation keys and values, mirroring the Kubernetes
	// rules: an optional DNS-subdomain prefix plus a name segment, max 63 chars. Annotation values are
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
		for i := range r.Application.Mounts {
			// A config mount is always read-only; see validateApplication.
			if r.Application.Mounts[i].Config != "" {
				r.Application.Mounts[i].ReadOnly = true
			}
		}
		if r.Application.ReloadPolicy == "" {
			r.Application.ReloadPolicy = ReloadRestart
		}
	case r.Config != nil:
		if r.Config.Mode == "" {
			r.Config.Mode = DefaultFileMode
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
	case r.Registry != nil:
		r.Registry.Server = normalizeRegistryServer(r.Registry.Server)
	}
}

// DefaultRegistryServer is the implicit registry host when a manifest names none. It mirrors
// registry.DefaultServer — duplicated rather than imported so this package stays free of service
// dependencies, and both sides must agree or a converged registry would diff.
const DefaultRegistryServer = "registry-1.docker.io"

// normalizeRegistryServer trims a registry host to its bare authority, matching
// what the registry service stores, so an unqualified manifest converges instead
// of drifting on every plan.
func normalizeRegistryServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return DefaultRegistryServer
	}
	server = strings.TrimPrefix(server, "https://")
	server = strings.TrimPrefix(server, "http://")
	return strings.TrimSuffix(server, "/")
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
	case KindRegistry:
		return r.validateRegistry()
	case KindConfig:
		return r.validateConfig()
	case KindMiddleware:
		return r.validateMiddleware()
	case KindVolume, KindStack, KindSecret, KindProject:
		return nil
	default:
		return fmt.Errorf("unknown kind %q", r.Kind)
	}
}

// validBuildMethod mirrors models.AppBuildMethod, duplicated for the same reason as validStrategy —
// the declarative package keeps its single dependency, and a test asserts the lists agree.
var validBuildMethod = map[string]bool{"auto": true, "dockerfile": true, "buildpack": true}

// validateSource checks a build-from-git source. Whether the repository can actually be cloned is a
// runtime question the build answers; this is the shape.
func (r *Resource) validateSource(src *SourceSpec) error {
	name := r.Metadata.Name
	if strings.TrimSpace(src.Git) == "" {
		return fmt.Errorf("application %q: source.git is required", name)
	}
	if src.BuildMethod != "" && !validBuildMethod[src.BuildMethod] {
		return fmt.Errorf("application %q: source.buildMethod %q must be auto, dockerfile or buildpack", name, src.BuildMethod)
	}
	// Buildpack-only settings on a Dockerfile build are silently ignored at build time, which reads
	// as the manifest not working. Say so instead.
	if src.BuildMethod == "dockerfile" && (src.Builder != "" || len(src.Buildpacks) > 0) {
		return fmt.Errorf("application %q: source.builder and source.buildpacks apply to buildpack builds, not buildMethod: dockerfile", name)
	}
	if src.Repository != "" && !nameRe.MatchString(src.Repository) {
		return fmt.Errorf("application %q: source.repository %q must be a resource name matching %s", name, src.Repository, nameRe)
	}
	return nil
}

// validHTTPMethod is the set a route may be narrowed to. A typo here would silently drop every
// request that used the method the author meant, which is the kind of thing found in production.
var validHTTPMethod = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "CONNECT": true, "OPTIONS": true, "TRACE": true,
}

// validStrategy mirrors models.DeployStrategy. It is duplicated rather than imported so the
// declarative package keeps its one dependency; a test asserts the two lists agree.
var validStrategy = map[string]bool{"recreate": true, "rolling": true, "canary": true}

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

// registryServerRe matches a registry authority: a dotted host with an optional
// port ("ghcr.io", "registry.example.com:5000", "localhost:5000").
var registryServerRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

func (r *Resource) validateRegistry() error {
	reg := r.Registry
	if reg == nil {
		return fmt.Errorf("registry %q: spec is required", r.Metadata.Name)
	}
	// normalize() has already stripped any scheme and defaulted the host, so what
	// is left must be a bare authority.
	if !registryServerRe.MatchString(reg.Server) {
		return fmt.Errorf("registry %q: server %q must be a host, optionally with a port (no scheme, no path)", r.Metadata.Name, reg.Server)
	}
	// A password is optional in the manifest: a credential may be declared in git
	// and have its token set out-of-band (once, in the UI). Creating it then fails
	// loudly at apply time rather than silently storing an empty secret.
	if strings.TrimSpace(reg.Username) == "" && strings.TrimSpace(reg.Password) != "" {
		return fmt.Errorf("registry %q: username is required when a password is set", r.Metadata.Name)
	}
	return nil
}

func (r *Resource) validateApplication() error {
	a := r.Application
	if a == nil {
		return fmt.Errorf("application %q: spec is required", r.Metadata.Name)
	}
	hasImage, hasSource := strings.TrimSpace(a.Image) != "", a.Source != nil
	switch {
	case !hasImage && !hasSource:
		return fmt.Errorf("application %q: one of image or source is required", r.Metadata.Name)
	case hasImage && hasSource:
		// Both would leave the engine to pick, and whichever it picked would be the wrong one half
		// the time — better to say so than to build an image the manifest also tells us to pull.
		return fmt.Errorf("application %q: image and source are mutually exclusive — an app either pulls an image or builds one", r.Metadata.Name)
	case hasSource:
		if err := r.validateSource(a.Source); err != nil {
			return err
		}
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
	// Shape only: whether the account is allowed at all depends on the target workspace's
	// security profile, which the app service checks on apply.
	if _, err := models.NormalizeRunAsUser(a.RunAsUser); err != nil {
		return fmt.Errorf("application %q: runAsUser %q: %w", r.Metadata.Name, a.RunAsUser, err)
	}
	if a.ExternalLabel != "" && !nameRe.MatchString(a.ExternalLabel) {
		return fmt.Errorf("application %q: externalLabel %q must be a DNS label", r.Metadata.Name, a.ExternalLabel)
	}
	if a.Registry != "" && !nameRe.MatchString(a.Registry) {
		return fmt.Errorf("application %q: registry %q must be a resource name matching %s", r.Metadata.Name, a.Registry, nameRe)
	}
	if a.Strategy != "" && !validStrategy[a.Strategy] {
		return fmt.Errorf("application %q: strategy %q must be recreate, rolling or canary", r.Metadata.Name, a.Strategy)
	}
	if a.ReloadPolicy != "" && a.ReloadPolicy != ReloadRestart && a.ReloadPolicy != ReloadNone {
		return fmt.Errorf("application %q: reloadPolicy %q must be %q or %q", r.Metadata.Name, a.ReloadPolicy, ReloadRestart, ReloadNone)
	}
	for _, mt := range a.Mounts {
		if err := r.validateMount(mt); err != nil {
			return err
		}
	}
	if err := r.validateMountOverlap(); err != nil {
		return err
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

func hasNonEmpty(s []string) bool {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// validateMiddleware checks the envelope only. The rule itself is deliberately not validated here:
// the gateway's middleware catalogue already owns that schema, and a second copy in this package
// would drift the moment the catalogue gains a field. An invalid rule fails on apply, where the
// catalogue validates it — the same place an unpullable image fails.
func (r *Resource) validateMiddleware() error {
	mw := r.Middleware
	if mw == nil {
		return fmt.Errorf("middleware %q: spec is required", r.Metadata.Name)
	}
	if strings.TrimSpace(mw.Type) == "" {
		return fmt.Errorf("middleware %q: type is required", r.Metadata.Name)
	}
	for _, p := range mw.Paths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("middleware %q: paths must not contain an empty entry", r.Metadata.Name)
		}
	}
	return nil
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
	for _, m := range rt.Methods {
		if !validHTTPMethod[strings.ToUpper(strings.TrimSpace(m))] {
			return fmt.Errorf("route %q: method %q is not an HTTP method", r.Metadata.Name, m)
		}
	}
	// The chain is ordered and executed in order, so a repeated name is a mistake
	// rather than a stronger policy — and the gateway would run it twice.
	seen := map[string]bool{}
	for _, name := range rt.Middlewares {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("route %q: middlewares must not contain an empty name", r.Metadata.Name)
		}
		if seen[name] {
			return fmt.Errorf("route %q: middleware %q is listed twice", r.Metadata.Name, name)
		}
		seen[name] = true
	}
	return nil
}

// validateMount enforces the one-source rule and the config-only field rules.
// Setting key/mode without a config is an error rather than ignored, matching the
// strict decoding used everywhere else.
func (r Resource) validateMount(mt MountSpec) error {
	name := r.Metadata.Name
	switch {
	case mt.Volume == "" && mt.Config == "":
		return fmt.Errorf("application %q: mount must set exactly one of volume or config", name)
	case mt.Volume != "" && mt.Config != "":
		return fmt.Errorf("application %q: mount sets both volume %q and config %q; exactly one is allowed", name, mt.Volume, mt.Config)
	}
	if !strings.HasPrefix(mt.Path, "/") {
		return fmt.Errorf("application %q: mount path %q must be absolute", name, mt.Path)
	}
	if mt.Config == "" {
		if mt.Key != "" {
			return fmt.Errorf("application %q: mount key %q is only valid with a config", name, mt.Key)
		}
		if mt.Mode != "" {
			return fmt.Errorf("application %q: mount mode %q is only valid with a config", name, mt.Mode)
		}
		return nil
	}
	if !nameRe.MatchString(mt.Config) {
		return fmt.Errorf("application %q: mount config %q must be a resource name matching %s", name, mt.Config, nameRe)
	}
	if mt.Key != "" {
		if err := validateConfigKey(mt.Key); err != nil {
			return fmt.Errorf("application %q: mount key: %w", name, err)
		}
		if strings.HasSuffix(mt.Path, "/") {
			return fmt.Errorf("application %q: mount path %q must be a file path when key is set", name, mt.Path)
		}
	}
	if mt.Mode != "" {
		if err := validateFileMode(mt.Mode); err != nil {
			return fmt.Errorf("application %q: mount mode: %w", name, err)
		}
	}
	return nil
}

// validateMountOverlap rejects a mount nested inside a volume, which the volume
// would shadow. Config mounts are projected per file, so one config path inside
// another is the normal directory-prefix form and is left alone.
func (r Resource) validateMountOverlap() error {
	ms := r.Application.Mounts
	for i := range ms {
		if ms[i].Volume == "" {
			continue
		}
		outer := strings.TrimSuffix(ms[i].Path, "/")
		for j := range ms {
			if i == j || outer == "" {
				continue
			}
			inner := strings.TrimSuffix(ms[j].Path, "/")
			if inner != "" && inner != outer && strings.HasPrefix(inner, outer+"/") {
				return fmt.Errorf("application %q: mount path %q is nested inside volume mount %q, which would shadow it", r.Metadata.Name, ms[j].Path, ms[i].Path)
			}
		}
	}
	return nil
}

// validateConfig enforces the file-key, size, mode and delimiter rules.
func (r Resource) validateConfig() error {
	c := r.Config
	if len(c.Data) == 0 {
		return fmt.Errorf("config %q: data must declare at least one file", r.Metadata.Name)
	}
	total := 0
	for k, v := range c.Data {
		if err := validateConfigKey(k); err != nil {
			return fmt.Errorf("config %q: %w", r.Metadata.Name, err)
		}
		if len(v) > MaxConfigFileBytes {
			return fmt.Errorf("config %q: file %q is %d bytes, over the %d-byte limit (Docker caps a config object at 500 KB, so a larger file would validate here and fail at deploy in cluster mode)", r.Metadata.Name, k, len(v), MaxConfigFileBytes)
		}
		total += len(v)
	}
	if total > MaxConfigTotalBytes {
		return fmt.Errorf("config %q: total content is %d bytes, over the %d-byte limit", r.Metadata.Name, total, MaxConfigTotalBytes)
	}
	if err := validateFileMode(c.Mode); err != nil {
		return fmt.Errorf("config %q: %w", r.Metadata.Name, err)
	}
	if len(c.Delimiters) > 0 {
		if len(c.Delimiters) != 2 {
			return fmt.Errorf("config %q: delimiters must be exactly two entries", r.Metadata.Name)
		}
		if c.Delimiters[0] == "" || c.Delimiters[1] == "" {
			return fmt.Errorf("config %q: delimiters must both be non-empty", r.Metadata.Name)
		}
		if c.Delimiters[0] == c.Delimiters[1] {
			return fmt.Errorf("config %q: delimiters must differ from each other", r.Metadata.Name)
		}
	}
	return nil
}

// validateConfigKey constrains a file key to a relative path: no absolute paths,
// no traversal, no empty segments.
func validateConfigKey(key string) error {
	if key == "" {
		return fmt.Errorf("file key must not be empty")
	}
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

// validateFileMode accepts a 3- or 4-digit octal mode and rejects setuid, setgid
// and the sticky bit.
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

// validateReferences checks cross-resource integrity once the whole set is
// known: mounts point at declared volumes, domains target declared apps, and an
// app's stack exists.
func (s *ResourceSet) validateReferences() error {
	for _, r := range s.list {
		switch {
		case r.Application != nil:
			for _, mt := range r.Application.Mounts {
				if mt.Config != "" {
					if !s.Has(KindConfig, mt.Config) {
						return fmt.Errorf("application %q: mount references unknown config %q", r.Metadata.Name, mt.Config)
					}
					continue
				}
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
