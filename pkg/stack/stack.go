// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

// Package platformstack installs and updates Miabi's own stack directly against the Docker API, with
// every component tagged io.miabi.managed-by=miabi. Compose owns what Compose created, so Miabi could
// never truly self-update while Compose held the lifecycle. It runs from the CLI, outside the container.
package stack

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/miabi-io/miabi/pkg/stack/docker"
	"github.com/miabi-io/miabi/pkg/stack/saferollout"
)

// CurrentVersion is the manifest schema version this build writes.
const CurrentVersion = 1

// ErrNotInstalled means there is no stack manifest — nothing was installed by the
// CLI here. It does NOT mean there is no Miabi: a Compose install has no manifest
// either, and callers say so.
var ErrNotInstalled = errors.New("no Miabi stack manifest found")

// ErrLegacyConfig means the manifest is at the pre-rename LegacyConfigPath. It is deliberately NOT
// an ErrNotInstalled: this host *is* installed, and telling its operator otherwise sends them
// looking for a Compose stack that was never there.
var ErrLegacyConfig = errors.New("the stack manifest is at its pre-1.8 path")

// Container names. They match examples/compose/compose.yaml deliberately: an operator's muscle
// memory (`docker logs miabi`) keeps working across both install paths, and Phase 1's
// name shield already knows them.
const (
	ContainerControlPlane = "miabi"
	ContainerPostgres     = "miabi-postgres"
	ContainerRedis        = "miabi-redis"
	ContainerGateway      = "miabi-gateway"
)

// Volumes. The mb- prefix marks them as Miabi's own, which dockerimport's name
// shield already recognizes, so they are never offered for import.
const (
	VolumePGData           = "mb-platform-pgdata"
	VolumeRedisData        = "mb-platform-redisdata"
	VolumeLogs             = "mb-platform-logs"
	VolumeGatewayCerts     = "mb-platform-gateway-certs"
	VolumeGatewayProviders = "mb-platform-gateway-providers"

	// VolumeGatewayConfig is NO LONGER CREATED: the gateway config is a bind-mounted
	// file on the host, not a copy in a volume. It survives here only so Teardown can
	// clean it up on installs that predate the change.
	VolumeGatewayConfig = "mb-platform-gateway-config"
)

const helperImage = "busybox:1.36"

// Default images. Overridable in the manifest; pinned here so a bare `miabi setup` is reproducible
// rather than tracking whatever :latest happens to be. NOTE the tags carry no leading "v": git tags do
// (v0.11.0), Docker tags do not — the git form produced an image reference that does not exist.
const (
	DefaultPostgresImage = "postgres:17-alpine"
	DefaultRedisImage    = "redis:7-alpine"
	DefaultGatewayImage  = "jkaninda/goma-gateway:0.14.0"
	// DefaultRunnerImage floats on :latest, unlike every other default here. The runner is the one image
	// the stack does not RUN — it only names what a CI runner should be enrolled with — so it carries none
	// of the reproducibility weight, and install.sh passes the version this release was tested against.
	DefaultRunnerImage = "miabi/runner:latest"
	DefaultNetwork     = "miabi"
	DefaultSubnet      = "10.63.0.0/16"
	// DefaultInternalNetwork carries the platform's own traffic. It is separate from DefaultNetwork
	// because that one is the shared proxy fabric: every routed app joins it, so anything reachable
	// there is reachable from a tenant container — and Postgres holds one superuser password for the
	// whole control plane.
	DefaultInternalNetwork = "miabi-internal"
	// DefaultInternalSubnet sits BELOW DefaultSubnet, not above: 10.64.0.0/12 is the workspace
	// allocator's pool (MIABI_NETWORK_POOL_CIDR), so 10.64.0.0/16 would collide with the first
	// workspace block Miabi hands out.
	DefaultInternalSubnet = "10.62.0.0/16"
)

// Service installs and updates the stack.
type Service struct {
	dc  docker.Client
	log func(format string, args ...any)
	// manifestPath is where miabi.yaml lives, AS THIS PROCESS SEES IT. The gateway's
	// config sits beside it, so the Service needs to know the directory.
	manifestPath string
}

func New(dc docker.Client, log func(string, ...any), manifestPath string) *Service {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Service{dc: dc, log: log, manifestPath: manifestPath}
}

// Defaults returns a manifest with everything the caller did not supply. miabiImage
// is the control-plane image, which has no sensible default — it is the version being
// installed, and the CLI knows it (it is that version).
func Defaults(miabiImage string) *Manifest {
	return &Manifest{
		Version:         CurrentVersion,
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

// Normalize fills in derived fields and validates what the caller must supply. It is
// called by Install and Update, so a hand-edited manifest gets the same treatment as
// a generated one.
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
	// The two must differ, or the split silently does nothing: every component would land back on the
	// proxy network and a hand-edited manifest would report a private stack it does not have.
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
		// Absent means on. Resolved here rather than at the use site so Save writes it
		// back explicitly — the manifest should state what it does, not leave it implied.
		on := true
		m.HostProc = &on
	}
	if err := m.normalizeRegistry(); err != nil {
		return err
	}
	if err := m.normalizeGateway(); err != nil {
		return err
	}
	if err := m.normalizeEnv(); err != nil {
		return err
	}
	return m.GenerateSecrets()
}

// Seeded gateway.env defaults, written into the manifest so they are discoverable
// rather than folklore.
const (
	// DefaultGomaLogLevel matches Goma's own default; writing it down makes the knob
	// visible. Real effect, measured: info emits 28 lines and 0 DEBUG on boot, debug
	// emits 51 lines and 22 DEBUG.
	DefaultGomaLogLevel = "info"

	envGomaLogLevel = "GOMA_LOG_LEVEL"

	// DefaultGomaAnalytics turns on the per-request event stream Goma publishes to Redis. On, because the
	// consumer runs by default and otherwise every analytics dashboard sits empty. Seeded into gateway.env
	// rather than the spec, since a spec variable becomes reserved and would refuse any operator value.
	DefaultGomaAnalytics = "true"

	envGomaAnalytics = "GOMA_ANALYTICS_ENABLED"
)

// gomaLogLevels are the values Goma acts on. Validated here because Goma does NOT
// reject an unknown one — it silently falls back to info, so a typo leaves an operator
// waiting for debug output that was never coming.
var gomaLogLevels = []string{"debug", "trace", "info", "warn", "error", "off"}

// seedGatewayEnvDefaults fills in what the operator should see, without overwriting
// anything they set.
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

// normalizeGateway defaults the config file name and validates gateway.env. The reserved set is derived
// from gatewaySpec, exactly as normalizeEnv derives the control plane's from controlPlaneSpec — so adding
// a variable to the gateway's spec automatically makes it un-spoofable here, with no list to keep in sync.
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

// reservedEnvPrefixes are settings the manifest models with a dedicated field, so setting them through env:
// would create a second source of truth that can silently disagree with the first. A bare
// MIABI_REGISTRY_ENABLED in env: would turn the registry on while `miabi stack status` still said it was off.
var reservedEnvPrefixes = []string{"MIABI_REGISTRY_"}

// Seeded env defaults, written into the manifest rather than applied invisibly, so `env:` shows an
// operator the two knobs they are most likely to want and where to change them. Both stay ordinary
// editable entries — neither needs to agree with anything else in the manifest.
const (
	DefaultTimezone = "UTC"
	// DefaultLogLevel matches what the control plane would pick anyway in production;
	// writing it down makes it discoverable instead of folklore.
	DefaultLogLevel = "info"

	envTimezone = "TZ"
	envLogLevel = "MIABI_LOG_LEVEL"
)

// logLevels are the values the control plane accepts. Validating here turns a typo into an instant, precise
// error instead of a control plane that crash-loops until the health gate gives up. "off" is absent
// deliberately — the logging library cannot honour it. A test asserts this list agrees with the config package's.
var logLevels = []string{"debug", "info", "warn", "warning", "error"}

// seedEnvDefaults fills in the entries an operator should see, without overwriting
// anything they have set.
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
}

// normalizeEnv validates the operator's extra environment variables and refuses any Miabi sets itself. The
// reserved set is DERIVED from the control plane's spec, not hand-written, which would be wrong the moment
// someone adds a variable. Refusing outright means there is never a duplicate key resolved by an ordering rule.
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
		for _, p := range reservedEnvPrefixes {
			if strings.HasPrefix(key, p) {
				return fmt.Errorf("env: %s is configured by its own manifest section, not by env "+
					"(use `registry: {enabled: true, host: …}`, or --registry on the command line)", key)
			}
		}
		// The gateway's config-encryption key must reach BOTH containers (Miabi encrypts what Goma decrypts).
		// Setting it here would give it to the control plane only, and Goma would read a config it cannot decrypt
		// — routing broken, no obvious cause. It has exactly one home.
		if key == gomaConfigEncryptionKey {
			return fmt.Errorf("env: %s belongs under `gateway.env` — set it there and Miabi "+
				"gives it to BOTH the gateway and the control plane, which is what it needs "+
				"(Miabi encrypts the config Goma decrypts)", key)
		}
		clean[key] = v
	}

	// Validate the seeded knobs. A bad log level would otherwise reach the control
	// plane, which rejects it at startup — so the container crash-loops and the install
	// fails two minutes later with "did not become healthy", nowhere near the cause.
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

// normalizeEmails resolves the two addresses an install needs — the Let's Encrypt contact and the seeded admin's
// login — letting each stand in for the other, since in practice one person is both. Only when NEITHER is given
// do we invent admin@<domain>. admin_email is consumed on FIRST boot only; changing it later renames nothing.
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

// validEmail is deliberately shallow: exactly one @, something either side, and a dot in the domain. Anything
// stricter rejects perfectly valid addresses, and the real check is that Let's Encrypt accepts it — which
// happens far from here, so a typo caught now saves an ACME failure that looks nothing like a typo.
func validEmail(s string) bool {
	local, domain, ok := strings.Cut(s, "@")
	return ok && local != "" && strings.Contains(domain, ".") &&
		!strings.Contains(domain, "@") && !strings.ContainsAny(s, " \t")
}

// normalizeControlURL defaults the control URL to the panel's public URL and checks it is one. A bad value fails
// far from the mistake: the panel works, and only later does an agent refuse to connect or a gateway fetch no
// routes. The trailing slash is trimmed because the agent trims it too, so the manifest matches reality.
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

// normalizeRegistry derives and validates the registry's hostname. The validation is not busywork: the registry
// host gets a public DNS record and its OWN TLS certificate, so a nonsense value makes the gateway ask Let's
// Encrypt for a name that cannot exist — burning rate limit and failing far from the mistake.
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

// validHostname accepts a dotted DNS name: at least two labels, alphanumerics and
// hyphens, no leading/trailing dot or hyphen.
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

// Converge brings the stack to the manifest's desired state in dependency order, and is safe to re-run:
// a component whose spec already matches is left alone. Postgres and Redis must be *healthy* before the
// control plane boots; the gateway goes last, so an incomplete stack fails closed.
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

	// Before any component: the gateway config must exist on the host and parse. It is bind-mounted, so it has
	// to be there before the container is created — and the panel's own route lives in it, so a typo is worth
	// catching now rather than as a health timeout after the database is already up.
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

// ConvergeData brings up only the stateful components the control plane depends on and stops there. It
// exists for disaster recovery: a restore must load a dump into an EMPTY database, and a control plane
// booting first would AutoMigrate and collide with the dump's own CREATE TABLE statements.
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
	Name string
	// Image is where this component's image lives in the manifest, so update can
	// write a new pin back to the right field.
	Image func(*Manifest) *string
	Build func(m *Manifest, name, image string) docker.RunSpec
	// Test says whether a rollout may run a second copy alongside the live one.
	// False for Postgres (two servers cannot open one data directory) and for the
	// control plane (a second one would run migrations against the live database).
	Test bool
	// WaitHealthy blocks the converge until this component is healthy. True for the
	// two the control plane depends on.
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
			// Without this, converge only checked that `docker run` returned — so a
			// control plane crash-looping on a bad database password still printed
			// "✓ Miabi is up". The install reported success while the panel was down.
			WaitHealthy: true,
		},
		{
			Name:  ContainerGateway,
			Image: func(m *Manifest) *string { return &m.Images.Gateway },
			Build: gatewaySpec,
			Test:  true, // it holds the ports; prove the new image boots before taking them
			// Goma answers /healthz, so converge can wait for it to actually SERVE rather than merely not having
			// exited yet. It is the last component and the only public one: an install that reports success while the
			// gateway is not routing is an install that reports success while the panel is unreachable.
			WaitHealthy: true,
		},
	}
}

// Component returns the named component, or false. Exported so the CLI can validate
// `miabi upgrade <component>` before touching anything.
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

// ensureNetwork creates both fabrics: the shared proxy network routed apps join, and the private one
// the platform's own components talk over. The private one is labelled platform-internal, which is what
// the app-facing APIs refuse to attach a container to — the name is the operator's to change.
func (s *Service) ensureNetwork(ctx context.Context, m *Manifest) error {
	for _, n := range []struct {
		cfg  NetworkConfig
		role string
	}{
		{m.Network, docker.RoleControlPlane},
		{m.InternalNetwork, docker.RolePlatformInternal},
	} {
		_, err := s.dc.EnsureNetworkSpec(ctx, docker.NetworkSpec{
			Name:   n.cfg.Name,
			Driver: "bridge",
			Subnet: n.cfg.Subnet,
			Labels: docker.PlatformLabels(n.role, docker.ManagedByMiabi, nil),
		})
		if err != nil {
			return fmt.Errorf("ensure network %q: %w", n.cfg.Name, err)
		}
	}
	return nil
}

// migrateNetworks moves an already-running stack onto the private network without restarting anything,
// and is a no-op once every component is where the manifest says.
//
// It has to run BEFORE the component loop. Networks are part of specHash, so the split recreates all
// four containers — but they are recreated in dependency order, and between Postgres and the control
// plane the old control plane would be talking to a database that had just left its network. Worse,
// `miabi upgrade miabi-postgres` recreates exactly one component and would leave the panel down until
// someone ran a full converge. Attaching live first means every component can reach every other one on
// BOTH networks, so any recreate order — or an interrupted converge — is safe.
//
// The trailing disconnect is for the container the loop then leaves alone (spec hash already current,
// e.g. a converge that died between the attach and the recreate): without it, it would keep an
// attachment to the proxy network that the manifest says it must not have.
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
		// Only ever the proxy network, and only when the spec says this component has no business on
		// it. Disconnecting anything else would be this function guessing at state it did not put there.
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

// ensureContainer creates the component if absent, replaces it if its spec changed, and leaves it alone
// otherwise. "Changed" is a hash of the run spec, stamped as a label: comparing field by field would mean
// re-deriving Docker's normalization, and every miss would be a spurious recreate — a restart for Postgres.
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

	// Accept an image already on the host when the registry is unreachable: an
	// air-gapped install, or one running a locally built / pre-loaded image, has
	// nothing to pull from and does not need to.
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

// ensureImage pulls, falling back to a copy already on the host, and reports the
// fallback through the install log rather than the global logger.
func ensureImage(ctx context.Context, dc docker.Client, ref string, log func(string, ...any)) error {
	return saferollout.EnsureImage(ctx, dc, ref, nil, func(f string, a ...any) {
		log("  "+f, a...)
	})
}

// Rollout replaces one component with a new image and returns the manifest field to
// persist. It is the update path; Converge is the install path.
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
