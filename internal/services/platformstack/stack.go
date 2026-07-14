// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package platformstack installs and updates Miabi's own stack — the network, the
// volumes, Postgres, Redis, the gateway and the control plane — directly against the
// Docker API, with every component tagged io.miabi.managed-by=miabi.
//
// Why this exists, when deploy/compose.yaml already works:
//
// Compose owns what Compose created. A container Miabi recreates out-of-band is
// silently reverted by the next `docker compose up -d`, which reads compose.yaml and
// sees drift. So Miabi could never truthfully "update itself" while Compose held the
// lifecycle — the update would survive until the operator's next routine command.
//
// The fix is not to have Miabi patch a file Compose owns. It is to change the owner.
// Everything here is tagged managed-by=miabi, which the label contract
// (internal/docker/labels.go) already defines as "Miabi created it and may freely
// recreate it".
//
// The decisive property is that this package runs from the CLI, on the host, OUTSIDE
// the control-plane container. That is precisely the actor needed to replace the
// control plane — something in-container code structurally cannot be, since
// `docker stop miabi` kills the process doing the stopping. It is what makes
// self-update possible at all.
//
// Compose remains fully supported; see docs. The two paths are distinguished by
// io.miabi.managed-by, and Miabi refuses to act on components it does not own.
package platformstack

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/services/saferollout"
)

// CurrentVersion is the manifest schema version this build writes.
const CurrentVersion = 1

// ErrNotInstalled means there is no stack manifest — nothing was installed by the
// CLI here. It does NOT mean there is no Miabi: a Compose install has no manifest
// either, and callers say so.
var ErrNotInstalled = errors.New("no Miabi stack manifest found")

// Container names. They match deploy/compose.yaml deliberately: an operator's muscle
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

// helperImage is a tiny image used for one-shot probes (the port check).
const helperImage = "busybox:1.36"

// Default images. Overridable in the manifest; pinned here so a bare `miabi install`
// is reproducible rather than tracking whatever :latest happens to be.
//
// NOTE the tags carry no leading "v". Git tags do (v0.11.0); Docker tags do not
// (0.11.0) — install.sh spells this out as ${GOMA_VERSION#v}. Writing the git form
// here produced an image reference that simply does not exist, and the install got
// all the way to the last component before failing on it.
const (
	DefaultPostgresImage = "postgres:17-alpine"
	DefaultRedisImage    = "redis:7-alpine"
	DefaultGatewayImage  = "jkaninda/goma-gateway:0.11.0"
	DefaultRunnerImage   = "miabi/runner:0.0.7"
	DefaultNetwork       = "miabi"
	DefaultSubnet        = "10.63.0.0/16"
)

// Service installs and updates the stack.
type Service struct {
	dc  docker.Client
	log func(format string, args ...any)
	// manifestPath is where stack.yaml lives, AS THIS PROCESS SEES IT. The gateway's
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
		Version: CurrentVersion,
		Network: NetworkConfig{Name: DefaultNetwork, Subnet: DefaultSubnet},
		Images: Images{
			Miabi:    miabiImage,
			Postgres: DefaultPostgresImage,
			Redis:    DefaultRedisImage,
			Gateway:  DefaultGatewayImage,
			Runner:   DefaultRunnerImage,
		},
		Secrets: Secrets{AdminEmail: "admin@example.com"},
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
	if m.ACMEEmail == "" {
		m.ACMEEmail = "admin@" + m.Domain
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
}

// normalizeGateway defaults the config file name and validates gateway.env.
//
// The reserved set is derived from gatewaySpec, exactly as normalizeEnv derives the
// control plane's from controlPlaneSpec — so adding a variable to the gateway's spec
// automatically makes it un-spoofable here, with no list to keep in sync.
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

// reservedEnvPrefixes are settings the manifest models with a dedicated field, so
// setting them through env: would create a second source of truth that can silently
// disagree with the first. The registry has --registry / registry.host; a bare
// MIABI_REGISTRY_ENABLED in env: would turn it on while `miabi status` and the plan
// output both still said it was off.
var reservedEnvPrefixes = []string{"MIABI_REGISTRY_"}

// Seeded env: defaults. They are written into the manifest rather than applied
// invisibly, so `env:` shows an operator the two knobs they are most likely to want
// and where to change them. Both stay ordinary env: entries — editable, and not
// reserved — because neither needs to agree with anything else in the manifest.
const (
	DefaultTimezone = "UTC"
	// DefaultLogLevel matches what the control plane would pick anyway in production;
	// writing it down makes it discoverable instead of folklore.
	DefaultLogLevel = "info"

	envTimezone = "TZ"
	envLogLevel = "MIABI_LOG_LEVEL"
)

// logLevels are the values the control plane accepts. Validating here turns a typo
// into an instant, precise error instead of a control plane that crash-loops on a
// config error until the health gate gives up two minutes later.
//
// "off" is absent deliberately — see config.LogLevelFor: the logging library cannot
// honour it for the calls Miabi actually makes. A test asserts this list agrees with
// the config package's.
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

// normalizeEnv validates the operator's extra environment variables and refuses any
// that Miabi sets itself.
//
// The reserved set is DERIVED from the control plane's own spec, not hand-written.
// A hand-written list is wrong the moment someone adds a variable to controlPlaneSpec
// and forgets to update it — and the failure is invisible: a duplicate key in a
// container's environment resolves by an ordering rule nobody should have to know,
// so the operator's value might win, silently replacing the database password with
// something that cannot open the database.
//
// Refusing outright (rather than ignoring, or merging with a precedence rule) means
// there is never a duplicate key in the first place.
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
		// The gateway's config-encryption key must reach BOTH containers (Miabi encrypts
		// what Goma decrypts). Setting it here would give it to the control plane only,
		// and Goma would read a config it cannot decrypt — routing broken, no obvious
		// cause. It has exactly one home.
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

// validEnvName accepts a POSIX-ish environment variable name.
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

// normalizeControlURL defaults the control URL to the panel's public URL and checks
// it is one.
//
// A bad value here fails in a place far from the mistake: the panel itself works, and
// only later does an agent refuse to connect, or a node's gateway quietly fetch no
// routes — because the URL they were handed is not a URL. Catch it at install.
//
// The trailing slash is trimmed because the agent trims it too; leaving it in means
// the manifest says one thing and the running system another.
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

// normalizeRegistry derives and validates the registry's hostname.
//
// The validation is not busywork. The registry host gets a public DNS record and its
// OWN TLS certificate, so a nonsense value makes the gateway ask Let's Encrypt for a
// certificate for a name that cannot exist — which burns rate limit and fails in a
// place far from the mistake. install.sh validates it for the same reason (there, a
// stray "y" carried over from the preceding y/N prompt was the way it happened).
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

// dockerGID is the group that owns the Docker socket, so the control plane can talk
// to it without running as root. Best-effort: an empty result just means the
// container runs as its image default (root), which still works.
func dockerGID() string {
	fi, err := os.Stat("/var/run/docker.sock")
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return strconv.FormatUint(uint64(st.Gid), 10)
}

// Converge brings the stack to the manifest's desired state, in dependency order,
// and is safe to run repeatedly: a component whose spec already matches is left
// alone, so `miabi install` on a healthy stack is a no-op rather than a recreate.
//
// Ordering is not cosmetic. Postgres and Redis must be *healthy*, not merely started,
// before the control plane boots — it runs migrations against one and connects to the
// other on startup, and a control plane that comes up first just crash-loops until
// they do. The gateway goes last because it is the only component that publishes
// ports, so an incomplete stack fails closed rather than serving errors to the world.
func (s *Service) Converge(ctx context.Context, m *Manifest) error {
	if err := m.Normalize(); err != nil {
		return err
	}

	s.log("ensuring network %s (%s)", m.Network.Name, m.Network.Subnet)
	if err := s.ensureNetwork(ctx, m); err != nil {
		return err
	}

	s.log("ensuring volumes")
	if err := s.ensureVolumes(ctx); err != nil {
		return err
	}

	// Before any component: the gateway config must exist on the host and parse. It is
	// bind-mounted, so it has to be there before the container is created — and the
	// panel's own route lives in it, so a typo is worth catching now rather than as a
	// health timeout after the database is already up.
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

// component is one container in the stack.
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
			// Goma answers /healthz, so converge can wait for it to actually SERVE rather
			// than merely not having exited yet. It is the last component and the only
			// public one: an install that reports success while the gateway is not routing
			// is an install that reports success while the panel is unreachable.
			WaitHealthy: true,
		},
	}
}

// Component returns the named component, or false. Exported so the CLI can validate
// `miabi update <component>` before touching anything.
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

func (s *Service) ensureNetwork(ctx context.Context, m *Manifest) error {
	_, err := s.dc.EnsureNetworkSpec(ctx, docker.NetworkSpec{
		Name:   m.Network.Name,
		Driver: "bridge",
		Subnet: m.Network.Subnet,
		Labels: docker.PlatformLabels(docker.RoleControlPlane, docker.ManagedByMiabi, nil),
	})
	if err != nil {
		return fmt.Errorf("ensure network %q: %w", m.Network.Name, err)
	}
	return nil
}

func (s *Service) ensureVolumes(ctx context.Context) error {
	for name, role := range map[string]string{
		VolumePGData:           docker.RolePlatformDB,
		VolumeRedisData:        docker.RolePlatformCache,
		VolumeLogs:             docker.RoleControlPlane,
		VolumeGatewayCerts:     docker.RoleGateway,
		VolumeGatewayProviders: docker.RoleGateway,
	} {
		// Volumes carry role + part-of for inventory, but NOT protected: the label is
		// read by the container guard, and there is no such thing as "stopping" a
		// volume. See plans/platform-labels.md §3.
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

// ensureContainer creates the component if absent, replaces it if its spec changed,
// and leaves it alone otherwise.
//
// "Changed" is decided by a hash of the run spec, stamped on the container as a
// label. Comparing the spec to the running container field by field would mean
// re-deriving Docker's own normalization (it rewrites ports, mounts, env order), and
// every miss would show up as a spurious recreate — which for Postgres means an
// unnecessary restart of the database. A hash of what we asked for sidesteps all of
// it: same request, same hash, no action.
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
