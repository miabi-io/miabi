// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

// Package stack installs Miabi's own stack via the Docker API, since Miabi can't self-update while Compose owns it.
package stack

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/miabi-io/miabi/pkg/bundled"
	"github.com/miabi-io/miabi/pkg/stack/docker"
	"github.com/miabi-io/miabi/pkg/stack/saferollout"
)

// CurrentVersion is the manifest schema version this build writes.
const CurrentVersion = 1

// It does not mean there is no Miabi: a Compose install has no manifest either.
var ErrNotInstalled = errors.New("no Miabi stack manifest found")

// Not ErrNotInstalled: this host is installed, and saying otherwise sends the operator hunting for Compose.
var ErrLegacyConfig = errors.New("the stack manifest is at its pre-1.8 path")

// Names match examples/compose/compose.yaml so `docker logs miabi` works on both install paths.
const (
	ContainerControlPlane = "miabi"
	ContainerPostgres     = "miabi-postgres"
	ContainerRedis        = "miabi-redis"
	ContainerGateway      = "miabi-gateway"
)

// The mb- prefix makes dockerimport's name shield skip them for import.
const (
	VolumePGData           = "mb-platform-pgdata"
	VolumeRedisData        = "mb-platform-redisdata"
	VolumeLogs             = "mb-platform-logs"
	VolumeGatewayCerts     = "mb-platform-gateway-certs"
	VolumeGatewayProviders = "mb-platform-gateway-providers"

	// No longer created; kept only so Teardown can clean it up on older installs.
	VolumeGatewayConfig = "mb-platform-gateway-config"
)

const helperImage = "busybox:1.36"

// DefaultGatewayImage is set at build time from versions.miabi.io (see package bundled).
var DefaultGatewayImage = bundled.GatewayImage()

// Pinned for reproducible installs; tags carry no leading "v" because Docker tags don't.
const (
	DefaultPostgresImage = bundled.PostgresImage
	DefaultRedisImage    = bundled.RedisImage
	DefaultRunnerImage   = "miabi/runner:latest"
	DefaultNetwork       = "miabi-proxy"
	// Pre-rename installs keep this name because their app containers are attached to it by name.
	LegacyNetwork = "miabi"
	DefaultSubnet = "10.63.0.0/16"
	// Separate from the shared proxy fabric, where anything is reachable from a tenant container.
	DefaultInternalNetwork = "miabi-internal"
	// Below DefaultSubnet because 10.64.0.0/16 would collide with the workspace allocator's pool.
	DefaultInternalSubnet = "10.62.0.0/16"
)

// Service installs and updates the stack.
type Service struct {
	dc  docker.Client
	log func(format string, args ...any)
	// As this process sees it; the gateway config sits beside it.
	manifestPath string
	// recreateNetworks permits converge to recreate a network to turn IPv6 on; see AllowNetworkRecreate.
	recreateNetworks bool
}

func New(dc docker.Client, log func(string, ...any), manifestPath string) *Service {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Service{dc: dc, log: log, manifestPath: manifestPath}
}

// miabiImage has no sensible default: it is the version the CLI is installing.
func Defaults(miabiImage string) *Manifest {
	return &Manifest{
		Version: CurrentVersion,
		// Existing flat files keep their shape until `miabi upgrade` converts them.
		kinded:          true,
		Network:         NetworkConfig{Name: DefaultNetwork, Subnet: DefaultSubnet},
		InternalNetwork: NetworkConfig{Name: DefaultInternalNetwork, Subnet: DefaultInternalSubnet},
		Images: Images{
			Miabi:    miabiImage,
			Postgres: DefaultPostgresImage,
			Redis:    DefaultRedisImage,
			Gateway:  DefaultGatewayImage,
			Runner:   DefaultRunnerImage,
		},
	}
}

// Called by Install and Update so hand-edited manifests get the same treatment as generated ones.
func (m *Manifest) Normalize() error {
	m.Domain = strings.TrimSpace(m.Domain)
	if m.Domain == "" {
		return errors.New("domain is required (the panel's public hostname, e.g. miabi.example.com)")
	}
	if m.WebURL == "" {
		m.WebURL = "https://" + m.Domain
	}
	if err := m.normalizeEmails(); err != nil {
		return err
	}
	if err := m.normalizeControlURL(); err != nil {
		return err
	}
	if m.Network.Name == "" {
		m.Network.Name = DefaultNetwork
	}
	if m.Network.Subnet == "" {
		m.Network.Subnet = DefaultSubnet
	}
	if m.InternalNetwork.Name == "" {
		m.InternalNetwork.Name = DefaultInternalNetwork
	}
	if m.InternalNetwork.Subnet == "" {
		m.InternalNetwork.Subnet = DefaultInternalSubnet
	}
	// Equal names would silently put every component back on the proxy network.
	if m.InternalNetwork.Name == m.Network.Name {
		return fmt.Errorf("internal_network.name and network.name are both %q — the private network "+
			"exists to keep the platform off the shared proxy network, so they cannot be the same",
			m.Network.Name)
	}
	if m.Images.Miabi == "" {
		return errors.New("images.miabi is required")
	}
	for dst, def := range map[*string]string{
		&m.Images.Postgres: DefaultPostgresImage,
		&m.Images.Redis:    DefaultRedisImage,
		&m.Images.Gateway:  DefaultGatewayImage,
		&m.Images.Runner:   DefaultRunnerImage,
	} {
		if *dst == "" {
			*dst = def
		}
	}
	if m.Version == 0 {
		m.Version = CurrentVersion
	}
	if m.DockerGID == "" {
		m.DockerGID = dockerGID()
	}
	if m.HostProc == nil {
		// Resolved here so Save writes it back and the manifest states what it does.
		on := true
		m.HostProc = &on
	}
	if err := m.normalizeRegistry(); err != nil {
		return err
	}
	if err := m.normalizeGateway(); err != nil {
		return err
	}
	// Must run before normalizeEnv, whose probe reads back what the spec emits.
	if err := m.normalizeInstall(); err != nil {
		return err
	}
	if err := m.normalizeEnv(); err != nil {
		return err
	}
	return m.GenerateSecrets()
}

const (
	// Matches Goma's own default; written down to make the knob visible.
	DefaultGomaLogLevel = "info"

	envGomaLogLevel = "GOMA_LOG_LEVEL"

	// On because the consumer runs by default; in gateway.env, not the spec, so operators can still override it.
	DefaultGomaAnalytics = "true"

	envGomaAnalytics = "GOMA_ANALYTICS_ENABLED"
)

// Goma silently falls back to info on an unknown level, so validate here.
var gomaLogLevels = []string{"debug", "trace", "info", "warn", "error", "off"}

func (m *Manifest) seedGatewayEnvDefaults() {
	if m.Gateway.Env == nil {
		m.Gateway.Env = map[string]string{}
	}
	if _, ok := m.Gateway.Env[envGomaLogLevel]; !ok {
		m.Gateway.Env[envGomaLogLevel] = DefaultGomaLogLevel
	}
	if _, ok := m.Gateway.Env[envGomaAnalytics]; !ok {
		m.Gateway.Env[envGomaAnalytics] = DefaultGomaAnalytics
	}
}

// The reserved set is derived from gatewaySpec, so new spec variables become un-spoofable with no list to sync.
func (m *Manifest) normalizeGateway() error {
	if m.Gateway.Config == "" {
		m.Gateway.Config = DefaultGatewayConfigFile
	}
	m.seedGatewayEnvDefaults()

	probe := *m
	probe.Gateway.Env = nil
	managed := map[string]bool{}
	for _, kv := range gatewaySpec(&probe, ContainerGateway, probe.Images.Gateway).Env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			managed[k] = true
		}
	}

	clean := make(map[string]string, len(m.Gateway.Env))
	for k, v := range m.Gateway.Env {
		key := strings.TrimSpace(k)
		if !validEnvName(key) {
			return fmt.Errorf("gateway.env: %q is not a valid variable name", key)
		}
		if managed[key] {
			return fmt.Errorf("gateway.env: %s is set by Miabi itself and cannot be overridden here.\n"+
				"  It is derived from the manifest (domain, acme_email, secrets) — change it there instead", key)
		}
		clean[key] = v
	}

	if lvl, ok := clean[envGomaLogLevel]; ok && lvl != "" {
		if !slices.Contains(gomaLogLevels, strings.ToLower(strings.TrimSpace(lvl))) {
			return fmt.Errorf("gateway.env: %s=%q is not a Goma log level (use %s). "+
				"Goma would not complain — it would silently fall back to info, and you would "+
				"wait for output that never came",
				envGomaLogLevel, lvl, strings.Join(gomaLogLevels, ", "))
		}
	}
	m.Gateway.Env = clean
	return nil
}

// Shared with the JSON Schema generator so editor values and accepted values cannot drift.
const (
	registryStorageFilesystem = "filesystem"
	registryStorageS3         = "s3"
)

// Otherwise a wrong value surfaces only at boot, or silently pins a console field.
func (m *Manifest) normalizeInstall() error {
	in := &m.Install

	if s := strings.ToLower(strings.TrimSpace(in.RegistryStorage)); s != "" {
		if s != registryStorageFilesystem && s != registryStorageS3 {
			return fmt.Errorf("registry.storage %q is not a storage driver (use %s or %s)",
				in.RegistryStorage, registryStorageFilesystem, registryStorageS3)
		}
		in.RegistryStorage = s
	}
	if p := in.Pool.SubnetPrefix; p != 0 && (p < 8 || p > 30) {
		return fmt.Errorf("networking.pool.subnetPrefix /%d is out of range (8–30)", p)
	}
	if hp := in.HostPorts; hp.Min != 0 || hp.Max != 0 {
		if hp.Min < 1 || hp.Max > 65535 {
			return fmt.Errorf("networking.hostPorts must fall within 1–65535 (got %d–%d)", hp.Min, hp.Max)
		}
		if hp.Min > hp.Max {
			return fmt.Errorf("networking.hostPorts.min %d is above max %d", hp.Min, hp.Max)
		}
	}
	if d := in.DNS.ReconcileMinutes; d < 0 {
		return fmt.Errorf("networking.dns.reconcileMinutes cannot be negative (got %d)", d)
	}
	proxies, err := normalizeTrustedProxies(in.GatewayTrustedProxies)
	if err != nil {
		return fmt.Errorf("gateway.trustedProxies: %w", err)
	}
	in.GatewayTrustedProxies = proxies
	if err := m.normalizeServerTrustedProxies(); err != nil {
		return err
	}
	return m.normalizeBackup()
}

// An empty value resolves to the private network so Save writes the default back visibly.
func (m *Manifest) normalizeServerTrustedProxies() error {
	if !m.kinded {
		return nil
	}
	in := &m.Install
	if raw, ok := m.Env[envTrustedProxies]; ok {
		if len(in.ServerTrustedProxies) > 0 {
			return fmt.Errorf("server.trustedProxies and server.env %s are both set — keep only "+
				"server.trustedProxies", envTrustedProxies)
		}
		in.ServerTrustedProxies = strings.Split(raw, ",")
		delete(m.Env, envTrustedProxies)
	}
	proxies, err := normalizeTrustedProxies(in.ServerTrustedProxies)
	if err != nil {
		return fmt.Errorf("server.trustedProxies: %w", err)
	}
	if len(proxies) == 0 {
		proxies = m.controlPlaneTrustedProxies()
	}
	in.ServerTrustedProxies = proxies
	return nil
}

// Goma would log a bad entry and drop proxy mode entirely, so requests carry the CDN's address again.
func normalizeTrustedProxies(in []string) ([]string, error) {
	var out []string
	for _, raw := range in {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		var bits int
		if ip := net.ParseIP(entry); ip == nil {
			_, ipnet, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, fmt.Errorf("%q is neither an IP address nor a CIDR", raw)
			}
			bits, _ = ipnet.Mask.Size()
		} else {
			bits = -1
		}
		if bits == 0 {
			return nil, fmt.Errorf("%q trusts every address, which lets any client choose its own IP "+
				"and walk past IP allowlists and rate limits; list your proxy's ranges instead", raw)
		}
		out = append(out, entry)
	}
	return out, nil
}

// A partial destination block would silently pin nothing, so it is rejected.
func (m *Manifest) normalizeBackup() error {
	b := m.Install.Backup
	if b == nil {
		return nil
	}
	given := 0
	for _, v := range []string{b.Destination.Bucket, b.Destination.AccessKey, b.Destination.SecretKey} {
		if strings.TrimSpace(v) != "" {
			given++
		}
	}
	if given != 0 && given != 3 {
		return errors.New("backup.destination needs bucket, accessKey and secretKey together — " +
			"with any one missing the control plane ignores the whole block and leaves the console in charge")
	}
	if b.Retention.Days < 0 || b.Retention.Max < 0 {
		return fmt.Errorf("backup.retention cannot be negative (max %d, days %d)", b.Retention.Max, b.Retention.Days)
	}
	return nil
}

// Setting these through env would create a second, silently disagreeing source of truth.
var reservedEnv = map[string]string{
	"MIABI_REGISTRY_ENABLED":        "registry: {enabled: true, host: …}, or --registry on the command line",
	"MIABI_REGISTRY_HOST":           "registry: {enabled: true, host: …}, or --registry on the command line",
	"MIABI_REGISTRY_STORAGE":        "spec.registry.storage in the install document",
	"MIABI_REGISTRY_PLATFORM_TOKEN": "spec.secrets.registryPlatformToken in the install document",
}

// Seeded into the manifest so `env:` shows the knobs; both stay ordinary editable entries.
const (
	DefaultTimezone = "UTC"
	DefaultLogLevel = "info"

	envTimezone       = "TZ"
	envLogLevel       = "MIABI_LOG_LEVEL"
	envTrustedProxies = "MIABI_TRUSTED_PROXIES"
)

// Validating here beats a crash-looping control plane; "off" is absent because the logger can't honour it.
var logLevels = []string{"debug", "info", "warn", "warning", "error"}

func (m *Manifest) seedEnvDefaults() {
	if m.Env == nil {
		m.Env = map[string]string{}
	}
	if _, ok := m.Env[envTimezone]; !ok {
		m.Env[envTimezone] = DefaultTimezone
	}
	if _, ok := m.Env[envLogLevel]; !ok {
		m.Env[envLogLevel] = DefaultLogLevel
	}
	if _, ok := m.Env[envTrustedProxies]; !ok && !m.kinded {
		m.Env[envTrustedProxies] = strings.Join(m.controlPlaneTrustedProxies(), ",")
	}
}

// The gateway is the only peer on the private network; unset, forwarded headers would be believed from anyone.
func (m *Manifest) controlPlaneTrustedProxies() []string {
	proxies := []string{m.InternalNetwork.Subnet}
	if m.InternalNetwork.IPv6 != nil && *m.InternalNetwork.IPv6 {
		// Docker allocates the network's IPv6 subnet from the ULA range when none is configured.
		proxies = append(proxies, "fc00::/7")
	}
	return proxies
}

// The reserved set is derived from the control plane's spec; refusing avoids duplicate keys resolved by order.
func (m *Manifest) normalizeEnv() error {
	m.seedEnvDefaults()

	// Build the spec with NO user env, and read back the keys it sets.
	probe := *m
	probe.Env = nil
	managed := map[string]bool{}
	for _, kv := range controlPlaneSpec(&probe, ContainerControlPlane, probe.Images.Miabi).Env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			managed[k] = true
		}
	}

	clean := make(map[string]string, len(m.Env))
	for k, v := range m.Env {
		key := strings.TrimSpace(k)
		if key == "" {
			return errors.New("env: an entry has an empty name")
		}
		if !validEnvName(key) {
			return fmt.Errorf("env: %q is not a valid variable name "+
				"(letters, digits and underscore; may not start with a digit)", key)
		}
		if managed[key] {
			return fmt.Errorf("env: %s is set by Miabi itself and cannot be overridden here.\n"+
				"  It is derived from the manifest (domain, secrets, images, network) — change it there instead", key)
		}
		if field, ok := reservedEnv[key]; ok {
			return fmt.Errorf("env: %s is configured by its own manifest section, not by env (use %s)", key, field)
		}
		// The key must reach both containers; set here only the control plane would get it and Goma couldn't decrypt.
		if key == gomaConfigEncryptionKey {
			return fmt.Errorf("env: %s has exactly one home — spec.secrets.gomaConfigEncryptionKey "+
				"in the install document, or gateway.env in the older flat manifest. Set it there and "+
				"Miabi gives it to BOTH the gateway and the control plane, which is what it needs "+
				"(Miabi encrypts the config Goma decrypts)", key)
		}
		clean[key] = v
	}

	// A bad level would otherwise crash-loop the control plane and fail the install much later.
	if lvl, ok := clean[envLogLevel]; ok && lvl != "" {
		if !slices.Contains(logLevels, strings.ToLower(strings.TrimSpace(lvl))) {
			return fmt.Errorf("env: %s=%q is not a log level (use %s)",
				envLogLevel, lvl, strings.Join([]string{"debug", "info", "warn", "error"}, ", "))
		}
	}
	m.Env = clean
	return nil
}

func validEnvName(k string) bool {
	for i, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Each address stands in for the other; admin_email is consumed on first boot only.
func (m *Manifest) normalizeEmails() error {
	acme := strings.TrimSpace(m.ACMEEmail)
	admin := strings.TrimSpace(m.Secrets.AdminEmail)

	switch {
	case acme == "" && admin == "":
		acme = "admin@" + m.Domain
		admin = acme
	case acme == "":
		acme = admin
	case admin == "":
		admin = acme
	}

	for label, addr := range map[string]string{"acme_email": acme, "admin_email": admin} {
		if !validEmail(addr) {
			return fmt.Errorf("%s %q is not an email address", label, addr)
		}
	}
	m.ACMEEmail, m.Secrets.AdminEmail = acme, admin
	return nil
}

// Deliberately shallow: stricter rejects valid addresses, and Let's Encrypt does the real check.
func validEmail(s string) bool {
	local, domain, ok := strings.Cut(s, "@")
	return ok && local != "" && strings.Contains(domain, ".") &&
		!strings.Contains(domain, "@") && !strings.ContainsAny(s, " \t")
}

// The trailing slash is trimmed because the agent trims it too.
func (m *Manifest) normalizeControlURL() error {
	m.ControlURL = strings.TrimRight(strings.TrimSpace(m.ControlURL), "/")
	if m.ControlURL == "" {
		m.ControlURL = m.WebURL
		return nil
	}
	u, err := url.Parse(m.ControlURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("control_url %q is not a URL — remote nodes and agents dial it, "+
			"so it must be absolute (https://miabi.example.com)", m.ControlURL)
	}
	return nil
}

// A bad host makes the gateway request an impossible certificate, burning ACME rate limit.
func (m *Manifest) normalizeRegistry() error {
	if !m.Registry.Enabled {
		return nil
	}
	m.Registry.Host = strings.TrimSpace(strings.ToLower(m.Registry.Host))
	if m.Registry.Host == "" {
		m.Registry.Host = "registry." + m.Domain
	}
	if !validHostname(m.Registry.Host) {
		return fmt.Errorf("registry host %q is not a valid hostname (expected something like registry.%s)",
			m.Registry.Host, m.Domain)
	}
	if m.Registry.Host == m.Domain {
		return fmt.Errorf("the registry host cannot be the panel's own hostname (%s) — "+
			"they are separate names, each with its own certificate", m.Domain)
	}
	return nil
}

func validHostname(h string) bool {
	if len(h) == 0 || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 ||
			strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
			if !isAlnum && r != '-' {
				return false
			}
		}
	}
	return true
}

// Safe to re-run; Postgres and Redis must be healthy before the control plane, gateway last so it fails closed.
func (s *Service) Converge(ctx context.Context, m *Manifest) error {
	if err := m.Normalize(); err != nil {
		return err
	}

	s.log("ensuring networks %s (%s) and %s (%s)",
		m.Network.Name, m.Network.Subnet, m.InternalNetwork.Name, m.InternalNetwork.Subnet)
	if err := s.ensureNetwork(ctx, m); err != nil {
		return err
	}
	if err := s.migrateNetworks(ctx, m); err != nil {
		return err
	}

	s.log("ensuring volumes")
	if err := s.ensureVolumes(ctx); err != nil {
		return err
	}

	// It is bind-mounted and holds the panel's own route, so it must exist and parse before anything is created.
	if err := s.EnsureGatewayConfig(ctx, m); err != nil {
		return err
	}

	for _, c := range s.components(m) {
		if err := s.ensureContainer(ctx, m, c); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return nil
}

// For disaster recovery: a control plane booting first would AutoMigrate and collide with the dump's CREATE TABLEs.
func (s *Service) ConvergeData(ctx context.Context, m *Manifest) error {
	if err := m.Normalize(); err != nil {
		return err
	}
	s.log("ensuring networks %s (%s) and %s (%s)",
		m.Network.Name, m.Network.Subnet, m.InternalNetwork.Name, m.InternalNetwork.Subnet)
	if err := s.ensureNetwork(ctx, m); err != nil {
		return err
	}
	if err := s.migrateNetworks(ctx, m); err != nil {
		return err
	}
	s.log("ensuring volumes")
	if err := s.ensureVolumes(ctx); err != nil {
		return err
	}
	for _, c := range s.components(m) {
		if c.Name != ContainerPostgres && c.Name != ContainerRedis {
			continue
		}
		if err := s.ensureContainer(ctx, m, c); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return nil
}

type component struct {
	Name  string
	Image func(*Manifest) *string
	Build func(m *Manifest, name, image string) docker.RunSpec
	// False for Postgres (one data directory) and the control plane (a second would migrate the live DB).
	Test        bool
	WaitHealthy bool
}

// components in dependency order.
func (s *Service) components(m *Manifest) []component {
	return []component{
		{
			Name:        ContainerPostgres,
			Image:       func(m *Manifest) *string { return &m.Images.Postgres },
			Build:       postgresSpec,
			Test:        false, // one data directory, one server
			WaitHealthy: true,
		},
		{
			Name:        ContainerRedis,
			Image:       func(m *Manifest) *string { return &m.Images.Redis },
			Build:       redisSpec,
			Test:        true,
			WaitHealthy: true,
		},
		{
			Name:  ContainerControlPlane,
			Image: func(m *Manifest) *string { return &m.Images.Miabi },
			Build: controlPlaneSpec,
			Test:  false, // a second control plane would migrate the live database
			// Without the health gate a control plane crash-looping on a bad DB password still reported success.
			WaitHealthy: true,
		},
		{
			Name:  ContainerGateway,
			Image: func(m *Manifest) *string { return &m.Images.Gateway },
			Build: gatewaySpec,
			Test:  true, // it holds the ports; prove the new image boots before taking them
			// The gateway is the only public component; reporting success while it isn't routing hides an unreachable panel.
			WaitHealthy: true,
		},
	}
}

// Exported so the CLI can validate `miabi upgrade <component>` before touching anything.
func (s *Service) Component(m *Manifest, name string) (component, bool) {
	for _, c := range s.components(m) {
		if c.Name == name {
			return c, true
		}
	}
	return component{}, false
}

// ComponentNames lists the updatable components, in dependency order.
func (s *Service) ComponentNames(m *Manifest) []string {
	cs := s.components(m)
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// The private network is guarded by its platform-internal label, since the name is the operator's to change.
func (s *Service) ensureNetwork(ctx context.Context, m *Manifest) error {
	for _, n := range []struct {
		cfg  NetworkConfig
		role string
	}{
		{m.Network, docker.RoleProxyNetwork},
		{m.InternalNetwork, docker.RolePlatformInternal},
	} {
		// Set explicitly so the network never inherits an unmanifested daemon default, as latent IPv6 once did.
		ipv6 := n.cfg.IPv6 != nil && *n.cfg.IPv6
		spec := docker.NetworkSpec{
			Name:       n.cfg.Name,
			Driver:     "bridge",
			Subnet:     n.cfg.Subnet,
			EnableIPv6: &ipv6,
			Labels:     docker.PlatformLabels(n.role, docker.ManagedByMiabi, nil),
		}
		if ipv6 {
			st, err := s.dc.InspectNetwork(ctx, n.cfg.Name)
			switch {
			case errors.Is(err, docker.ErrNotFound):
			case err != nil:
				return fmt.Errorf("inspect network %q: %w", n.cfg.Name, err)
			case !st.EnableIPv6 && s.recreateNetworks:
				old, off := spec, false
				old.EnableIPv6 = &off
				if err := s.recreateNetwork(ctx, spec, old); err != nil {
					return err
				}
				continue
			case !st.EnableIPv6:
				s.log("network %s has no IPv6; run `miabi setup --ipv6` to recreate it with IPv6", n.cfg.Name)
			}
		}
		_, err := s.dc.EnsureNetworkSpec(ctx, spec)
		if err != nil {
			return fmt.Errorf("ensure network %q: %w", n.cfg.Name, err)
		}
	}
	return nil
}

// Attaches live before the component loop so any recreate order, or an interrupted converge, stays connected.
func (s *Service) migrateNetworks(ctx context.Context, m *Manifest) error {
	for _, c := range s.components(m) {
		cur, err := s.dc.InspectContainer(ctx, c.Name)
		if errors.Is(err, docker.ErrNotFound) {
			continue // a fresh install: the component loop creates it on the right networks
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", c.Name, err)
		}
		want := c.Build(m, c.Name, *c.Image(m)).Networks
		for _, n := range want {
			if attachedTo(cur, n) {
				continue
			}
			s.log("  %-14s joining %s", c.Name, n)
			if err := s.dc.NetworkConnect(ctx, n, c.Name, nil); err != nil {
				return fmt.Errorf("attach %s to %s: %w", c.Name, n, err)
			}
		}
		if slices.Contains(want, m.Network.Name) || !attachedTo(cur, m.Network.Name) {
			continue
		}
		// Only the proxy network, and only when the spec forbids it; anything else would be guessing.
		s.log("  %-14s leaving %s", c.Name, m.Network.Name)
		if err := s.dc.NetworkDisconnect(ctx, m.Network.Name, c.Name, false); err != nil {
			return fmt.Errorf("detach %s from %s: %w", c.Name, m.Network.Name, err)
		}
	}
	return nil
}

func attachedTo(c docker.Container, network string) bool {
	for _, n := range c.Networks {
		if n.Name == network {
			return true
		}
	}
	return false
}

func (s *Service) ensureVolumes(ctx context.Context) error {
	for name, role := range map[string]string{
		VolumePGData:           docker.RolePlatformDB,
		VolumeRedisData:        docker.RolePlatformCache,
		VolumeLogs:             docker.RoleControlPlane,
		VolumeGatewayCerts:     docker.RoleGateway,
		VolumeGatewayProviders: docker.RoleGateway,
	} {

		labels := map[string]string{
			docker.LabelPartOf:    docker.PartOfMiabi,
			docker.LabelRole:      role,
			docker.LabelManagedBy: docker.ManagedByMiabi,
		}
		if _, err := s.dc.CreateVolume(ctx, name, labels, 0); err != nil {
			return fmt.Errorf("ensure volume %q: %w", name, err)
		}
	}
	return nil
}

// Compares a spec-hash label, since re-deriving Docker's normalization would cause spurious recreates.
func (s *Service) ensureContainer(ctx context.Context, m *Manifest, c component) error {
	image := *c.Image(m)
	spec := c.Build(m, c.Name, image)
	want := specHash(spec)
	spec.Labels[docker.LabelSpecHash] = want

	cur, err := s.dc.InspectContainer(ctx, c.Name)
	switch {
	case err != nil && !errors.Is(err, docker.ErrNotFound):
		return fmt.Errorf("inspect: %w", err)

	case err == nil && cur.Labels[docker.LabelSpecHash] == want && cur.State == "running":
		s.log("  %-14s up to date", c.Name)
		return nil

	case err == nil:
		s.log("  %-14s changed — recreating", c.Name)
		_ = s.dc.RemoveContainer(ctx, c.Name, true)

	default:
		s.log("  %-14s creating", c.Name)
	}

	// Accept a local image when the registry is unreachable, e.g. air-gapped or pre-loaded installs.
	if err := ensureImage(ctx, s.dc, image, s.log); err != nil {
		return err
	}
	if _, err := s.dc.RunContainer(ctx, spec); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if c.WaitHealthy {
		s.log("  %-14s waiting for health", c.Name)
		if err := saferollout.WaitHealthy(ctx, s.dc, c.Name, 2*time.Minute); err != nil {
			return err
		}
	}
	return nil
}

func ensureImage(ctx context.Context, dc docker.Client, ref string, log func(string, ...any)) error {
	return saferollout.EnsureImage(ctx, dc, ref, nil, func(f string, a ...any) {
		log("  "+f, a...)
	})
}

func (s *Service) Rollout(ctx context.Context, m *Manifest, name, newImage string, onPhase func(string, error)) error {
	if err := m.Normalize(); err != nil {
		return err
	}
	c, ok := s.Component(m, name)
	if !ok {
		return fmt.Errorf("unknown component %q (have: %s)", name, strings.Join(s.ComponentNames(m), ", "))
	}
	if err := s.EnsureGatewayConfig(ctx, m); err != nil {
		return err
	}
	return saferollout.Run(ctx, s.dc, saferollout.Spec{
		Name:  c.Name,
		Image: newImage,
		Build: func(n, img string) docker.RunSpec {
			sp := c.Build(m, n, img)
			sp.Labels[docker.LabelSpecHash] = specHash(sp)
			return sp
		},
		Test:     c.Test,
		Rollback: true,
		OnPhase:  onPhase,
	})
}
