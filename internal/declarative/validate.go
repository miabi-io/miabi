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
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"

	"github.com/miabi-io/miabi/pkg/sealed"
)

// The per-file cap matters against Docker's 500 KB config-object limit, since projection is per file.
const (
	MaxConfigFileBytes  = 256 * 1024
	MaxConfigTotalBytes = 512 * 1024
	DefaultFileMode     = "0644"
	ReloadRestart       = "restart"
	ReloadNone          = "none"
)

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	portNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// configKeyRe matches a relative file key, optionally nested in subdirectories.
	configKeyRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*)?(/[A-Za-z0-9._-]+)*$`)
	hostnameRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	// Mirrors the Kubernetes label rules; annotation values are exempt.
	labelKeyRe   = regexp.MustCompile(`^([a-z0-9]([-a-z0-9.]*[a-z0-9])?/)?[a-zA-Z0-9]([-a-zA-Z0-9_.]*[a-zA-Z0-9])?$`)
	labelValRe   = regexp.MustCompile(`^[a-zA-Z0-9]([-a-zA-Z0-9_.]*[a-zA-Z0-9])?$`)
	validEngines = map[models.DBEngine]bool{
		models.DBEnginePostgres: true, models.DBEngineMySQL: true, models.DBEngineMariaDB: true,
		models.DBEngineRedis: true, models.DBEngineMongoDB: true, models.DBEngineLibSQL: true,
	}
	validTLS       = map[string]bool{"acme": true, "custom": true, "off": true}
	validDomainTLS = map[string]bool{"acme": true, "custom": true}
	placements     = map[string]bool{"auto": true, "dedicated": true, "shared": true}
)

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
		r.Application.adoptDeprecated()
	case r.Config != nil:
		if r.Config.Mode == "" {
			r.Config.Mode = DefaultFileMode
		}
	case r.Database != nil:
		d := r.Database
		if d.Placement != nil && d.Placement.legacyInstance != "" && d.Instance == "" {
			d.Instance, d.Placement = d.Placement.legacyInstance, nil
		}
		if d.Instance == "" {
			d.Instance = "auto"
		}
	case r.Route != nil:
		r.Route.adoptDeprecated()
		if r.Route.Path == "" {
			r.Route.Path = "/"
		}
	case r.Registry != nil:
		r.Registry.Server = normalizeRegistryServer(r.Registry.Server)
	case r.GitRepository != nil:
		g := r.GitRepository
		g.URL = NormalizeGitURL(g.URL)
		if g.Auth == "" {
			g.Auth = "public"
			if g.SecretRef != "" {
				g.Auth = "token"
			}
		}
	case r.GitSource != nil:
		g := r.GitSource
		g.Repo, g.Ref, g.Path = strings.TrimSpace(g.Repo), strings.TrimSpace(g.Ref), strings.TrimSpace(g.Path)
		if g.Ref == "" {
			g.Ref = "main"
		}
		if g.Path == "" {
			g.Path = "."
		}
		if g.SyncPolicy == "" {
			g.SyncPolicy = "manual"
		}
		if g.DeletePolicy == "" {
			g.DeletePolicy = DeletePolicyOrphan
		}
	}
}

// One given alongside its new spelling stays put, for validate to refuse rather than pick a winner.
func (a *ApplicationSpec) adoptDeprecated() {
	if a.DeprecatedStrategy != "" && a.Strategy() == "" {
		if a.Deployment == nil {
			a.Deployment = &DeploymentSpec{}
		}
		a.Deployment.Strategy, a.DeprecatedStrategy = a.DeprecatedStrategy, ""
	}
	if a.DeprecatedRunAsUser != "" && a.RunAsUser() == "" {
		if a.Security == nil {
			a.Security = &SecuritySpec{}
		}
		a.Security.RunAsUser, a.DeprecatedRunAsUser = a.DeprecatedRunAsUser, ""
	}
	if sec := a.Security; sec != nil && sec.DeprecatedAddCapabilities != nil && a.AddCapabilities() == nil {
		if sec.Capabilities == nil {
			sec.Capabilities = &CapabilitiesSpec{}
		}
		sec.Capabilities.Add, sec.DeprecatedAddCapabilities = sec.DeprecatedAddCapabilities, nil
	}
}

func (r *RouteSpec) adoptDeprecated() {
	if t := r.TLS; t != nil && t.legacyMode == "off" && t.Enabled == nil {
		off := false
		t.Enabled = &off
	}
	if r.DeprecatedCertificate == "" && r.DeprecatedTLSProvider == "" {
		return
	}
	if r.TLS == nil {
		r.TLS = &RouteTLSSpec{}
	}
	if r.DeprecatedCertificate != "" && r.TLS.Certificate == "" {
		r.TLS.Certificate, r.DeprecatedCertificate = r.DeprecatedCertificate, ""
	}
	if r.DeprecatedTLSProvider != "" && r.TLS.Provider == "" {
		r.TLS.Provider, r.DeprecatedTLSProvider = r.DeprecatedTLSProvider, ""
	}
}

// DefaultRegistryServer duplicates registry.DefaultServer to avoid service deps; they must agree or registries diff.
const DefaultRegistryServer = "registry-1.docker.io"

// Matches what the registry service stores, so an unqualified manifest converges.
func normalizeRegistryServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return DefaultRegistryServer
	}
	server = strings.TrimPrefix(server, "https://")
	server = strings.TrimPrefix(server, "http://")
	return strings.TrimSuffix(server, "/")
}

// Manifests are untrusted input, so validation is strict.
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
	case KindGitSource:
		return r.validateGitSource()
	case KindGitRepository:
		return r.validateGitRepository()
	case KindConfig:
		return r.validateConfig()
	case KindMiddleware:
		return r.validateMiddleware()
	case KindCronJob:
		return r.validateCronJob()
	case KindJob:
		return r.validateJob()
	case KindSealedSecret:
		return r.validateSealedSecret()
	case KindStack:
		return r.validateStack()
	case KindVolume, KindSecret, KindProject:
		return nil
	default:
		return fmt.Errorf("unknown kind %q", r.Kind)
	}
}

// Duplicated to keep the package's single dependency; a test asserts the lists agree.
var validBuildMethod = map[string]bool{"auto": true, "dockerfile": true, "buildpack": true}

// Whether the repository can be cloned is a runtime question the build answers.
func (r *Resource) validateSource(src *SourceSpec) error {
	name := r.Metadata.Name
	if strings.TrimSpace(src.Git) == "" {
		return fmt.Errorf("application %q: source.git is required", name)
	}
	if src.BuildMethod != "" && !validBuildMethod[src.BuildMethod] {
		return fmt.Errorf("application %q: source.buildMethod %q must be auto, dockerfile or buildpack", name, src.BuildMethod)
	}
	// Buildpack settings on a Dockerfile build are silently ignored at build time, so refuse them.
	if src.BuildMethod == "dockerfile" && (src.Builder != "" || len(src.Buildpacks) > 0) {
		return fmt.Errorf("application %q: source.builder and source.buildpacks apply to buildpack builds, not buildMethod: dockerfile", name)
	}
	if src.Repository != "" && !nameRe.MatchString(src.Repository) {
		return fmt.Errorf("application %q: source.repository %q must be a resource name matching %s", name, src.Repository, nameRe)
	}
	return nil
}

// A typo would silently drop every request using the intended method.
var validHTTPMethod = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "CONNECT": true, "OPTIONS": true, "TRACE": true,
}

// Duplicated to keep the package's single dependency; a test asserts the lists agree.
var validStrategy = map[string]bool{"recreate": true, "rolling": true, "canary": true}

var validHealthcheckType = map[string]bool{"image": true, "none": true, "http": true, "command": true}

func validateHealthcheck(hc *HealthcheckSpec) error {
	if hc == nil {
		return nil
	}
	if !validHealthcheckType[hc.Type] {
		return fmt.Errorf("healthcheck.type %q must be http, command, image or none", hc.Type)
	}
	if hc.Type == "command" && strings.TrimSpace(hc.Command) == "" {
		return fmt.Errorf("healthcheck.command is required when healthcheck.type is command")
	}
	if hc.Path != "" && !strings.HasPrefix(hc.Path, "/") {
		return fmt.Errorf("healthcheck.path %q must start with /", hc.Path)
	}
	if hc.Port < 0 || hc.Port > 65535 {
		return fmt.Errorf("healthcheck.port %d must be between 1 and 65535", hc.Port)
	}
	if hc.IntervalSeconds < 0 || hc.TimeoutSeconds < 0 || hc.Retries < 0 || hc.StartPeriodSeconds < 0 {
		return fmt.Errorf("healthcheck intervals and retries cannot be negative")
	}
	return nil
}

var constraintRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+\s*(==|!=)\s*[^\s=!].*$`)

// Refuse service settings on a container: three replicas must not quietly run one.
func validateRuntime(name string, a *ApplicationSpec) error {
	runtime := a.Runtime()
	service := runtime == string(models.RuntimeService)
	switch {
	case runtime != "" && !models.ValidRuntimeKind(models.RuntimeKind(runtime)):
		return fmt.Errorf("application %q: deployment.runtime %q must be container or service", name, runtime)
	case !service && (a.Replicas() != 0 || a.Constraints() != nil || a.Update() != nil):
		return fmt.Errorf("application %q: deployment.replicas, deployment.update and placement.constraints need deployment.runtime: service", name)
	case a.Replicas() < 0 || a.Replicas() > models.MaxServiceReplicas:
		return fmt.Errorf("application %q: deployment.replicas %d must be between 1 and %d", name, a.Replicas(), models.MaxServiceReplicas)
	case service && len(a.Devices()) > 0:
		return fmt.Errorf("application %q: %w", name, models.ErrDevicesOnService)
	}
	for _, c := range a.Constraints() {
		if !constraintRe.MatchString(strings.TrimSpace(c)) {
			return fmt.Errorf("application %q: placement constraint %q must read <attribute>==<value> or <attribute>!=<value>", name, c)
		}
	}
	if u := a.Update(); u != nil && (u.Parallelism < 0 || u.DelaySeconds < 0) {
		return fmt.Errorf("application %q: deployment.update parallelism and delaySeconds cannot be negative", name)
	}
	return nil
}

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
	// Matches Kubernetes' 63-char name-segment limit, independent of the prefix.
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
	if !validDomainTLS[r.Domain.TLSMode()] {
		return fmt.Errorf("domain %q: tls.mode must be acme or custom", r.Metadata.Name)
	}
	return nil
}

var registryServerRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

func (r *Resource) validateSealedSecret() error {
	if r.SealedSecret == nil || r.SealedSecret.Value == "" {
		return fmt.Errorf("sealed secret %q: value is required (make one with `miabi secrets seal %s`)", r.Metadata.Name, r.Metadata.Name)
	}
	if _, _, err := sealed.Parse(r.SealedSecret.Value); err != nil {
		return fmt.Errorf("sealed secret %q: value must be sealed with `miabi secrets seal`: %w", r.Metadata.Name, err)
	}
	return nil
}

// Matches the credential store's ".git" suffix so an unsuffixed URL converges.
func NormalizeGitURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	if u != "" && !strings.HasSuffix(strings.ToLower(u), ".git") {
		u += ".git"
	}
	return u
}

func (r *Resource) validateGitRepository() error {
	g := r.GitRepository
	if g == nil {
		return fmt.Errorf("gitrepository %q: spec is required", r.Metadata.Name)
	}
	switch {
	case g.URL == "":
		return fmt.Errorf("gitrepository %q: url is required", r.Metadata.Name)
	case g.Auth != "public" && g.Auth != "token" && g.Auth != "ssh":
		return fmt.Errorf("gitrepository %q: auth %q must be public, token or ssh", r.Metadata.Name, g.Auth)
	case g.Auth == "public" && g.SecretRef != "":
		return fmt.Errorf("gitrepository %q: a public repository takes no secretRef", r.Metadata.Name)
	case g.Auth != "public" && g.SecretRef == "":
		return fmt.Errorf("gitrepository %q: auth %s needs secretRef, the Secret or SealedSecret holding the credential", r.Metadata.Name, g.Auth)
	case g.SecretRef != "" && !nameRe.MatchString(g.SecretRef):
		return fmt.Errorf("gitrepository %q: secretRef %q must be a resource name matching %s", r.Metadata.Name, g.SecretRef, nameRe)
	}
	return nil
}

func (r *Resource) validateGitSource() error {
	g := r.GitSource
	if g == nil {
		return fmt.Errorf("gitsource %q: spec is required", r.Metadata.Name)
	}
	switch {
	case (g.Repo == "") == (g.Repository == ""):
		return fmt.Errorf("gitsource %q: set exactly one of repo (a URL) and repository (a stored Git credential)", r.Metadata.Name)
	case g.Repository != "" && !nameRe.MatchString(g.Repository):
		return fmt.Errorf("gitsource %q: repository %q must be a resource name matching %s", r.Metadata.Name, g.Repository, nameRe)
	case g.SyncPolicy != "manual" && g.SyncPolicy != "auto":
		return fmt.Errorf("gitsource %q: syncPolicy %q must be manual or auto", r.Metadata.Name, g.SyncPolicy)
	case g.DeletePolicy != DeletePolicyOrphan && g.DeletePolicy != DeletePolicyCascade:
		return fmt.Errorf("gitsource %q: deletePolicy %q must be orphan or cascade", r.Metadata.Name, g.DeletePolicy)
	case strings.HasPrefix(g.Path, "/") || slices.Contains(strings.Split(g.Path, "/"), ".."):
		return fmt.Errorf("gitsource %q: path %q must be relative to the repository root", r.Metadata.Name, g.Path)
	}
	return nil
}

func (r *Resource) validateRegistry() error {
	reg := r.Registry
	if reg == nil {
		return fmt.Errorf("registry %q: spec is required", r.Metadata.Name)
	}
	// normalize() already stripped any scheme and defaulted the host.
	if !registryServerRe.MatchString(reg.Server) {
		return fmt.Errorf("registry %q: server %q must be a host, optionally with a port (no scheme, no path)", r.Metadata.Name, reg.Server)
	}
	// Password is optional: the token may be set out-of-band; creating without one fails loudly at apply.
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
	switch {
	case a.DeprecatedStrategy != "":
		return fmt.Errorf("application %q: strategy is the old spelling of deployment.strategy; set only deployment.strategy", r.Metadata.Name)
	case a.DeprecatedRunAsUser != "":
		return fmt.Errorf("application %q: runAsUser is the old spelling of security.runAsUser; set only security.runAsUser", r.Metadata.Name)
	case a.Security != nil && a.Security.DeprecatedAddCapabilities != nil:
		return fmt.Errorf("application %q: security.addCapabilities is the old spelling of security.capabilities.add; set only security.capabilities.add", r.Metadata.Name)
	}
	hasImage, hasSource := strings.TrimSpace(a.Image) != "", a.Source != nil
	switch {
	case !hasImage && !hasSource:
		return fmt.Errorf("application %q: one of image or source is required", r.Metadata.Name)
	case hasImage && hasSource:
		// Both would leave the engine to pick a winner.
		return fmt.Errorf("application %q: image and source are mutually exclusive — an app either pulls an image or builds one", r.Metadata.Name)
	case hasSource:
		if err := r.validateSource(a.Source); err != nil {
			return err
		}
	}
	if _, embedded := models.SplitImageRef(strings.TrimSpace(a.Image)); embedded != "" {
		if tag := strings.TrimSpace(a.Tag); tag != "" && tag != embedded {
			return fmt.Errorf("application %q: image %q already carries tag %q; drop it from image or remove tag: %q", r.Metadata.Name, a.Image, embedded, tag)
		}
	}
	if a.Digest != "" && !strings.HasPrefix(a.Digest, "sha256:") {
		return fmt.Errorf("application %q: digest must be a sha256: reference", r.Metadata.Name)
	}
	portNames := map[string]int{}
	for _, p := range a.Ports {
		if p.Container <= 0 || p.Container > 65535 {
			return fmt.Errorf("application %q: invalid container port %d", r.Metadata.Name, p.Container)
		}
		if p.Name != "" {
			if !ValidPortName(p.Name) {
				return fmt.Errorf("application %q: port %d name %q must be lowercase letters, digits and dashes, with at least one letter", r.Metadata.Name, p.Container, p.Name)
			}
			// One container port may serve tcp and udp under the same name.
			if other, dup := portNames[p.Name]; dup && other != p.Container {
				return fmt.Errorf("application %q: port name %q is used by ports %d and %d", r.Metadata.Name, p.Name, other, p.Container)
			}
			portNames[p.Name] = p.Container
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
	// Shape only: whether the account is allowed depends on the workspace profile, checked on apply.
	if _, err := models.NormalizeRunAsUser(a.RunAsUser()); err != nil {
		return fmt.Errorf("application %q: security.runAsUser %q: %w", r.Metadata.Name, a.RunAsUser(), err)
	}
	// Shape and allow-list only; the workspace gate is the app service's, as above.
	if a.Security != nil {
		add, err := models.NormalizeCapabilities(a.AddCapabilities())
		if err != nil {
			return fmt.Errorf("application %q: %w", r.Metadata.Name, err)
		}
		drop, err := models.NormalizeDropCapabilities(a.DropCapabilities())
		if err != nil {
			return fmt.Errorf("application %q: %w", r.Metadata.Name, err)
		}
		if err := models.CheckCapabilityConflict(add, drop); err != nil {
			return fmt.Errorf("application %q: %w", r.Metadata.Name, err)
		}
		if _, err := models.NormalizeDevices(a.Devices()); err != nil {
			return fmt.Errorf("application %q: %w", r.Metadata.Name, err)
		}
	}
	if err := validateHealthcheck(a.Healthcheck); err != nil {
		return fmt.Errorf("application %q: %w", r.Metadata.Name, err)
	}
	if a.ExternalLabel != "" && !nameRe.MatchString(a.ExternalLabel) {
		return fmt.Errorf("application %q: externalLabel %q must be a DNS label", r.Metadata.Name, a.ExternalLabel)
	}
	if a.Registry != "" && !nameRe.MatchString(a.Registry) {
		return fmt.Errorf("application %q: registry %q must be a resource name matching %s", r.Metadata.Name, a.Registry, nameRe)
	}
	if s := a.Strategy(); s != "" && !validStrategy[s] {
		return fmt.Errorf("application %q: deployment.strategy %q must be recreate, rolling or canary", r.Metadata.Name, s)
	}
	if err := validateRuntime(r.Metadata.Name, a); err != nil {
		return err
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

func (r *Resource) validateStack() error {
	st := r.Stack
	if st == nil {
		return nil
	}
	for _, k := range st.SecretEnv {
		if _, ok := st.Env[k]; !ok {
			return fmt.Errorf("stack %q: secretEnv %q is not declared in env", r.Metadata.Name, k)
		}
	}
	return nil
}

func (r *Resource) validateDatabase() error {
	d := r.Database
	if d == nil {
		return fmt.Errorf("database %q: spec is required", r.Metadata.Name)
	}
	if !validEngines[models.DBEngine(d.Engine)] {
		return fmt.Errorf("database %q: unsupported engine %q", r.Metadata.Name, d.Engine)
	}
	if d.Placement != nil && d.Placement.legacyInstance != "" {
		return fmt.Errorf("database %q: placement: %s is the old spelling of instance; set only instance", r.Metadata.Name, d.Placement.legacyInstance)
	}
	if !placements[d.Instance] {
		return fmt.Errorf("database %q: invalid instance %q", r.Metadata.Name, d.Instance)
	}
	if d.ReadOnlyUser != nil && *d.ReadOnlyUser && models.DBEngine(d.Engine) != models.DBEnginePostgres {
		return fmt.Errorf("database %q: readOnlyUser is available for postgres only", r.Metadata.Name)
	}
	if !models.EngineSupportsLogicalDatabases(models.DBEngine(d.Engine)) && d.Instance == "shared" {
		return fmt.Errorf("database %q: engine %q has no logical databases; instance cannot be 'shared'", r.Metadata.Name, d.Engine)
	}
	if res := d.Resources; res != nil {
		if res.Size != "" && (res.Memory != "" || res.CPU != "") {
			return fmt.Errorf("database %q: resources: name a size, or set memory and cpu, not both", r.Metadata.Name)
		}
		if res.Size != "" && !nameRe.MatchString(res.Size) {
			return fmt.Errorf("database %q: resources: size %q must be a size name matching %s", r.Metadata.Name, res.Size, nameRe)
		}
		if _, err := res.MemoryBytes(); err != nil {
			return fmt.Errorf("database %q: resources: %w", r.Metadata.Name, err)
		}
		if _, err := res.NanoCPUs(); err != nil {
			return fmt.Errorf("database %q: resources: %w", r.Metadata.Name, err)
		}
		if d.Instance == "shared" {
			return fmt.Errorf("database %q: resources size an instance of its own; a shared database runs with its host's", r.Metadata.Name)
		}
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

// The rule is validated by the gateway catalogue on apply; a copy here would drift.
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
	if err := r.validateRoutePort(); err != nil {
		return err
	}
	if err := r.validateRouteTLS(); err != nil {
		return err
	}
	for _, m := range rt.Methods {
		if !validHTTPMethod[strings.ToUpper(strings.TrimSpace(m))] {
			return fmt.Errorf("route %q: method %q is not an HTTP method", r.Metadata.Name, m)
		}
	}
	// A repeated name is a mistake, and the gateway would run it twice.
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

// Key/mode without a config is an error rather than ignored, matching strict decoding.
func (r *Resource) validateRoutePort() error {
	p := r.Route.Port
	if p.Number < 0 || p.Number > 65535 {
		return fmt.Errorf("route %q: port %d out of range", r.Metadata.Name, p.Number)
	}
	if p.Name != "" && !portNameRe.MatchString(p.Name) {
		return fmt.Errorf("route %q: port %q must be a number or the name of one of the app's ports", r.Metadata.Name, p.Name)
	}
	return nil
}

func (r *Resource) validateRouteTLS() error {
	rt, name := r.Route, r.Metadata.Name
	legacy := ""
	if rt.TLS != nil {
		legacy = rt.TLS.legacyMode
	}
	if legacy != "" && !validTLS[legacy] {
		return fmt.Errorf("route %q: tls must be a block (enabled, provider, certificate), or the old acme, custom or off", name)
	}
	// Left over only when both spellings were set.
	if rt.DeprecatedCertificate != "" {
		return fmt.Errorf("route %q: certificate is the old spelling of tls.certificate; set only tls.certificate", name)
	}
	if rt.DeprecatedTLSProvider != "" {
		return fmt.Errorf("route %q: tlsProvider is the old spelling of tls.provider; set only tls.provider", name)
	}
	mode, cert, provider := rt.TLSMode(), rt.TLSCertificate(), rt.TLSProvider()
	switch {
	case legacy == "custom" && strings.TrimSpace(cert) == "":
		return fmt.Errorf("route %q: tls: custom needs a certificate; set tls.certificate to the certificate to serve for its hosts", name)
	case legacy == "acme" && cert != "":
		return fmt.Errorf("route %q: certificate %q applies to tls: custom, but this route is tls: acme", name, cert)
	case mode == "off" && cert != "":
		return fmt.Errorf("route %q: tls.certificate %q needs TLS, but this route turns TLS off", name, cert)
	case mode == "off" && provider != "":
		return fmt.Errorf("route %q: tls.provider %q needs TLS, but this route turns TLS off", name, provider)
	case cert != "" && provider != "":
		return fmt.Errorf("route %q: tls.provider applies to ACME certificates, but this route serves tls.certificate %q; set one of them", name, cert)
	}
	if cert != "" && !nameRe.MatchString(cert) {
		return fmt.Errorf("route %q: certificate %q must be a resource name matching %s", name, cert, nameRe)
	}
	return nil
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

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

// Config mounts are projected per file, so one config path inside another is fine.
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

func (s *ResourceSet) validateReferences() error {
	for _, r := range s.list {
		if r.Kind == KindSealedSecret && s.Has(KindSecret, r.Metadata.Name) {
			return fmt.Errorf("secret %q is declared both as a Secret and as a SealedSecret; keep one", r.Metadata.Name)
		}
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
			app, ok := s.Get(string(KindApplication) + "/" + r.Route.App)
			if !ok {
				return fmt.Errorf("route %q: targets unknown application %q", r.Metadata.Name, r.Route.App)
			}
			if err := resolvePortName(r, app); err != nil {
				return err
			}
		}
	}
	return nil
}

// Resolved to the number at parse so plans compare it with the port the live route stores.
func resolvePortName(route, app Resource) error {
	name := route.Route.Port.Name
	if name == "" {
		return nil
	}
	for _, p := range app.Application.Ports {
		if p.Name == name {
			route.Route.Port = RoutePort{Number: p.Container}
			return nil
		}
	}
	return fmt.Errorf("route %q: port %q is not a port name of application %q", route.Metadata.Name, name, app.Metadata.Name)
}

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
