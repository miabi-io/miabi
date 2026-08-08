// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package edgegateway provisions the Goma Gateway container fronting an edge-gateway node's public
// ingress. Unlike port-forward nodes, such a node terminates TLS locally and serves its own routes,
// pulled from the control plane's HTTP provider using the node's own agent token.
package edgegateway

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/pkg/stack/selfcontainer"
	"gopkg.in/yaml.v3"
)

// ErrInvalidConfig is returned when a submitted gateway config is not valid YAML.
var ErrInvalidConfig = errors.New("gateway config is not valid YAML")

const (
	// ContainerName is the gateway container's name on the node.
	ContainerName        = "mb-node-gateway"
	CentralContainerName = "miabi-gateway"
	// CentralRoleValue is the docker.LabelRole value the compose gateway carries so
	// Miabi can find it by identity regardless of the compose project name.
	CentralRoleValue = "gateway"
	// TestContainer is the throwaway container a safe update starts to validate a
	// new image/config before promoting it over the live gateway.
	TestContainer = ContainerName + "-test"
	// RedisContainer is the per-node Redis a remote edge gateway uses for shared
	// cache + distributed rate limiting (the manager reuses the platform Redis).
	RedisContainer = "mb-node-gateway-redis"
	// ConfigVolume holds the rendered goma.yml; CertsVolume persists ACME certs.
	ConfigVolume = "mb-node-gateway-config"
	CertsVolume  = "mb-node-gateway-certs"
	// helperImage is a tiny image used to seed the config volume with goma.yml.
	helperImage = "busybox:1.36"
	// redisImageDefault is the per-node gateway Redis image (overridable via the
	// platform image catalog).
	redisImageDefault = "redis:7-alpine"
	configPath        = "/etc/goma"
	// DefaultConfigFile is Goma Gateway's default config path inside the container.
	DefaultConfigFile = configPath + "/goma.yml"
	// ProvidersVolume is a legacy per-node providers volume.
	ProvidersVolume = "mb-node-gateway-providers"
	providersPath   = configPath + "/providers"
	// gatewayRedisPasswordEnv is the env var the gateway's Redis password is
	// injected as; the rendered config references it (Goma expands ${...} at
	// runtime) so the stored/editable config holds no secret.
	gatewayRedisPasswordEnv = "GATEWAY_REDIS_PASSWORD"
	// updateObserveSeconds is how long a test container must stay up before the
	// new image is promoted over the live gateway.
	updateObserveSeconds = 25
)

// ConfigFilePath resolves the goma.yml path a gateway container reads, mirroring Goma's own precedence:
// an explicit --config/-c flag on the entrypoint or command wins, then GOMA_CONFIG_FILE, else the
// default /etc/goma/goma.yml. Used to copy an imported gateway's config.
func ConfigFilePath(entrypoint, cmd, env []string) string {
	args := append(append([]string{}, entrypoint...), cmd...)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" || a == "-c":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(a, "--config="):
			return strings.TrimPrefix(a, "--config=")
		case strings.HasPrefix(a, "-c="):
			return strings.TrimPrefix(a, "-c=")
		}
	}
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GOMA_CONFIG_FILE="); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return DefaultConfigFile
}

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// Service deploys and tears down node gateways.
type Service struct {
	workspaces    *repositories.WorkspaceRepository
	controlURL    string
	image         string
	network       string
	acmeEmail     string
	images        ImageResolver
	redisAddr     string // the platform Redis address (reused by the manager's gateway)
	redisPassword string // the platform Redis password
	configEncKey  string // GOMA_CONFIG_ENCRYPTION_KEY injected into every gateway ("" = off)
	// analyticsStream is the Redis stream request events are published to; empty
	// disables analytics in the rendered config (see analyticsEnabledFor).
	analyticsStream string
	// providersVolume is the Docker volume backing Miabi's own route-file directory. Empty means Miabi cannot
	// hand a gateway that directory, so the manager's gateway polls the HTTP provider like a remote node does.
	// See SetProvidersVolume.
	providersVolume string
}

func NewService(workspaces *repositories.WorkspaceRepository, controlURL, image, network, acmeEmail string) *Service {
	return &Service{
		workspaces: workspaces,
		controlURL: strings.TrimRight(controlURL, "/"),
		image:      image,
		network:    network,
		acmeEmail:  acmeEmail,
	}
}

// SetImageResolver wires the deployment-config resolver for the gateway/helper
// images (overrides the env-configured defaults when set).
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// SetConfigEncryptionKey wires the Goma config-encryption key. When non-empty it
// is injected into every edge gateway as GOMA_CONFIG_ENCRYPTION_KEY, so Goma
// encrypts sensitive parts of its config (middleware rules and TLS) at rest.
func (s *Service) SetConfigEncryptionKey(key string) { s.configEncKey = strings.TrimSpace(key) }

// SetRedis wires the platform Redis the manager's gateway reuses (a remote edge
// node runs its own per-node Redis instead). Empty addr disables Redis for the
// manager's gateway.
func (s *Service) SetRedis(addr, password string) {
	s.redisAddr = strings.TrimSpace(addr)
	s.redisPassword = password
}

// Default analytics sampling and stream cap, matching the platform stack's
// goma.yml so a gateway installed from the admin dashboard behaves the same as
// one from the shipped stack.
const (
	defaultAnalyticsSample = 1.0
	defaultAnalyticsMaxLen = 1_000_000
)

// SetAnalytics wires the Redis stream the gateway publishes request events to. Pass "" when the
// platform's analytics consumer is not running: a gateway writing to a stream nobody reads would fill
// Redis to maxLen and be discarded.
func (s *Service) SetAnalytics(stream string) { s.analyticsStream = strings.TrimSpace(stream) }

// analyticsEnabledFor reports whether the rendered config should turn analytics on for this node's gateway. It
// requires publishing to the *platform* Redis, the one Miabi's consumer reads: a remote edge node runs its own
// Redis, so events there reach no dashboard. It asks where the gateway runs, not which route provider it uses.
func (s *Service) analyticsEnabledFor(srv *models.Server) bool {
	return s.analyticsStream != "" && isManager(srv) && s.redisAddrFor(srv) != ""
}

func (s *Service) redisImg() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyRedis); r != "" {
			return r
		}
	}
	return redisImageDefault
}

// RedisEnabled reports whether the node's gateway has Redis wired: a per-node
// Redis for a remote edge node, or the platform Redis for the manager (when one
// is configured).
func (s *Service) RedisEnabled(srv *models.Server) bool { return s.redisAddrFor(srv) != "" }

// redisAddrFor returns the Redis address a node's gateway should use: the shared platform Redis for the manager,
// or the per-node Redis for a self-contained remote edge node. Empty when Redis is unavailable. Keyed on where
// the gateway runs, not its route provider — a manager on the HTTP provider still shares the platform Redis.
func (s *Service) redisAddrFor(srv *models.Server) string {
	if isManager(srv) {
		return s.redisAddr
	}
	return RedisContainer + ":6379"
}

func (s *Service) gatewayImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyGoma); r != "" {
			return r
		}
	}
	return s.image
}

// Image returns the gateway image that would be deployed for srv: the node's
// per-node override when set, else the resolved catalog/default image.
func (s *Service) Image(srv *models.Server) string {
	if srv != nil && strings.TrimSpace(srv.GatewayImage) != "" {
		return strings.TrimSpace(srv.GatewayImage)
	}
	return s.gatewayImage()
}

// ContainerNameFor returns the gateway container name to inspect/stream for a
// node: an imported gateway's tracked container, or the managed default
// (mb-node-gateway).
func ContainerNameFor(srv *models.Server) string {
	if srv != nil && strings.TrimSpace(srv.GatewayContainer) != "" {
		return strings.TrimSpace(srv.GatewayContainer)
	}
	return ContainerName
}

// LooksLikeGateway reports whether an image reference looks like a Goma gateway,
// used to surface import candidates.
func LooksLikeGateway(image string) bool {
	return strings.Contains(strings.ToLower(image), "goma")
}

func (s *Service) helperImg() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyHelper); r != "" {
			return r
		}
	}
	return helperImage
}

// --- Goma config schema (subset we render) ---

type gomaConfig struct {
	Version     int          `yaml:"version"`
	CertManager *certManager `yaml:"certManager,omitempty"`
	Gateway     gateway      `yaml:"gateway"`
}

// certManager follows Goma's multi-provider schema: named cert providers keyed by name, with
// defaultProvider naming the one used when a route's tls.provider is empty. A route opts a specific
// provider in via tls.provider: <name>, or out of TLS entirely via tls.provider: none.
type certManager struct {
	DefaultProvider string                  `yaml:"defaultProvider,omitempty"`
	Providers       map[string]certProvider `yaml:"providers,omitempty"`
}

type certProvider struct {
	Type string     `yaml:"type"` // acme | vault
	Acme *acmeBlock `yaml:"acme,omitempty"`
}

type acmeBlock struct {
	Email string `yaml:"email"`
}

type gateway struct {
	Log         *logBlock             `yaml:"log,omitempty"`
	TLS         *tlsBlock             `yaml:"tls,omitempty"`
	Redis       *redisBlock           `yaml:"redis,omitempty"`
	Networking  *networking           `yaml:"networking,omitempty"`
	Analytics   *analyticsBlock       `yaml:"analytics,omitempty"`
	EntryPoints map[string]entryPoint `yaml:"entryPoints,omitempty"`
	Reload      *reloadBlock          `yaml:"reload,omitempty"`
	Providers   providers             `yaml:"providers"`
}

// analyticsBlock makes the gateway publish one event per request to a Redis stream, which Miabi's consumer
// rolls up into the dashboards. Events carry no client IP — only a daily-salted visitor hash and, with a GeoIP
// database, a country resolved at the edge. GOMA_ANALYTICS_* on the container overrides whatever is here.
type analyticsBlock struct {
	Enabled bool   `yaml:"enabled"`
	Stream  string `yaml:"stream"`
	// Sample is the fraction of requests recorded (0..1). The dashboards scale
	// sampled counts back up, so lowering it on a very high-traffic edge trades
	// precision for load rather than losing whole panels.
	Sample float64 `yaml:"sample"`
	// MaxLen approximately caps the stream, so a stopped consumer cannot grow
	// Redis without bound.
	MaxLen int `yaml:"maxLen"`
}

// logBlock sets the edge gateway's log verbosity (Goma's gateway.log.level),
// e.g. debug | info | warn | error. Admins can change it from the node config.
type logBlock struct {
	Level string `yaml:"level"`
}

// reloadBlock enables Goma's on-demand reload endpoint, so Miabi can tell the edge gateway to pull its config
// immediately instead of waiting for the poll interval. The token is supplied via the GOMA_RELOAD_TOKEN env
// var, set to the node's gateway token at deploy, so the stored config holds no secret.
type reloadBlock struct {
	Enabled bool `yaml:"enabled"`
}

// networking carries Goma's DNS-cache tuning. clearOnReload flushes the local
// DNS cache after a route reload so a backend whose IP changed (e.g. a container
// re-created on redeploy) is re-resolved instead of served from a stale entry.
type networking struct {
	DNSCache dnsCache `yaml:"dnsCache"`
}

type dnsCache struct {
	TTL           int  `yaml:"ttl"`
	ClearOnReload bool `yaml:"clearOnReload"`
}

type tlsBlock struct {
	CertsDir string `yaml:"certsDir"`
}

// redisBlock points Goma at a Redis for shared caching + distributed rate
// limiting. The password references an env var so the stored config holds no
// secret (Goma expands ${...} at runtime).
type redisBlock struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password,omitempty"`
}

type entryPoint struct {
	Address string `yaml:"address"`
}

type providers struct {
	HTTP *httpProvider `yaml:"http,omitempty"`
	File *fileProvider `yaml:"file,omitempty"`
}

type httpProvider struct {
	Enabled       bool              `yaml:"enabled"`
	Endpoint      string            `yaml:"endpoint"`
	Interval      string            `yaml:"interval"`
	Timeout       string            `yaml:"timeout"`
	RetryAttempts int               `yaml:"retryAttempts"`
	RetryDelay    string            `yaml:"retryDelay"`
	Headers       map[string]string `yaml:"headers,omitempty"`
}

// fileProvider watches a directory of route/middleware YAML files. The manager
// uses this: it shares Miabi's providers volume, so routes Miabi writes
// there are picked up directly — no HTTP polling.
type fileProvider struct {
	Enabled   bool   `yaml:"enabled"`
	Directory string `yaml:"directory"`
	Watch     bool   `yaml:"watch"`
}

// apiKeyEnv is the env var the gateway token is injected as; the rendered config
// references it (Goma expands ${...} at runtime) so the stored/editable config
// holds no secret.
const apiKeyEnv = "INSTANCE_API_KEY"

// reloadTokenEnv is the env var Goma reads the reload-endpoint token from
// (GOMA_RELOAD_TOKEN). Set to the node's gateway token at deploy so Miabi, which
// holds the same token, can authenticate to the edge gateway's reload endpoint.
const reloadTokenEnv = "GOMA_RELOAD_TOKEN"

// configEncryptionKeyEnv is the env var Goma reads to encrypt sensitive parts of
// its config (middleware rules and TLS material) at rest. When Miabi is
// configured with a key it is injected into every edge gateway under this name.
const configEncryptionKeyEnv = "GOMA_CONFIG_ENCRYPTION_KEY"

// defaultCertProvider is the name of the ACME provider the node gateway ships
// with and uses by default. Routes opt into another provider via tls.provider:
// <name> (which an admin adds to the editable gateway config), or out via "none".
const defaultCertProvider = "acme"

// defaultLogLevel is the edge gateway's log verbosity in the rendered default
// config (Goma's gateway.log.level).
const defaultLogLevel = "info"

// isManager reports whether srv is the node the control plane itself runs on. This decides *infrastructure*:
// the manager's gateway shares the platform Redis and the providers volume, while every remote edge node is
// self-contained. Deliberately distinct from usesFileProvider, which is about how it learns its routes.
func isManager(srv *models.Server) bool {
	return srv != nil && srv.IsLocal
}

// usesFileProvider reports which route source the *rendered default* config uses. The file provider needs the
// gateway handed the very directory Miabi writes route files to — possible only on the manager, and only when
// Miabi knows the backing volume. Everything else polls HTTP. It is only the default; see effectiveFileProvider.
func (s *Service) usesFileProvider(srv *models.Server) bool {
	return isManager(srv) && s.providersVolume != ""
}

// SetProvidersVolume names the Docker volume backing Miabi's own route-file directory, so the manager's
// gateway can be given the same one. Leave it empty — a manual install whose mounts Miabi did not
// create — and the manager's gateway polls the HTTP provider instead.
func (s *Service) SetProvidersVolume(name string) { s.providersVolume = strings.TrimSpace(name) }

// DetectProvidersVolume returns the name of the Docker volume mounted at providerDir inside Miabi's own
// container, or "" when there is none. That is what tells a stack install from a manual one: only a
// named volume can be mounted into a second container, so only that lets the gateway watch the directory.
func DetectProvidersVolume(ctx context.Context, dc docker.Client, providerDir string) string {
	if dc == nil || strings.TrimSpace(providerDir) == "" {
		return ""
	}
	self := selfcontainer.Detect()
	if self == "" {
		return "" // not running in a container: nothing to share
	}
	cfg, err := dc.InspectContainerConfig(ctx, self)
	if err != nil {
		logger.Warn("edgegateway: could not inspect own container for the providers volume", "error", err)
		return ""
	}
	want := strings.TrimRight(providerDir, "/")
	for _, m := range cfg.Mounts {
		if strings.TrimRight(m.Destination, "/") != want {
			continue
		}
		if m.Type != "volume" || m.Name == "" {
			// A bind mount is a host path, not something Miabi can hand another
			// container by name — the gateway polls instead.
			logger.Info("edgegateway: route directory is not a named volume; the manager's gateway will poll the HTTP provider",
				"destination", m.Destination, "type", m.Type)
			return ""
		}
		return m.Name
	}
	return ""
}

// effectiveFileProvider reports whether srv's gateway *actually* reads routes from the providers volume,
// honouring a customised config rather than assuming the default. A manager gateway switched to the HTTP
// provider needs the same reload plumbing a remote node gets. An unparseable config falls back to the default.
func (s *Service) effectiveFileProvider(srv *models.Server) bool {
	if srv == nil || strings.TrimSpace(srv.GatewayConfigYAML) == "" {
		return s.usesFileProvider(srv)
	}
	var cfg gomaConfig
	if err := yaml.Unmarshal([]byte(srv.GatewayConfigYAML), &cfg); err != nil {
		return s.usesFileProvider(srv)
	}
	p := cfg.Gateway.Providers
	if p.File != nil && p.File.Enabled {
		return true
	}
	if p.HTTP != nil && p.HTTP.Enabled {
		return false
	}
	return s.usesFileProvider(srv)
}

// RenderConfig builds the default goma.yml a node gateway runs with: ACME for TLS and entry points on
// 80/443. The manager watches the shared providers volume directly; a remote edge node uses an HTTP
// provider pointed at this control plane, authenticated with ${INSTANCE_API_KEY}.
func (s *Service) RenderConfig(srv *models.Server) string {
	var prov providers
	if s.usesFileProvider(srv) {
		prov.File = &fileProvider{Enabled: true, Directory: providersPath, Watch: true}
	} else {
		prov.HTTP = &httpProvider{
			Enabled:       true,
			Endpoint:      fmt.Sprintf("%s/api/v1/provider/%s", s.controlURL, srv.Name),
			Interval:      "30s",
			Timeout:       "10s",
			RetryAttempts: 3,
			RetryDelay:    "2s",
			Headers:       map[string]string{"Authorization": "${" + apiKeyEnv + "}"},
		}
	}
	cfg := gomaConfig{
		Version: 2,
		CertManager: &certManager{
			DefaultProvider: defaultCertProvider,
			Providers: map[string]certProvider{
				defaultCertProvider: {Type: "acme", Acme: &acmeBlock{Email: s.acmeEmail}},
			},
		},
		Gateway: gateway{
			Log:        &logBlock{Level: defaultLogLevel},
			TLS:        &tlsBlock{CertsDir: configPath + "/certs"},
			Networking: &networking{DNSCache: dnsCache{TTL: 300, ClearOnReload: true}},
			EntryPoints: map[string]entryPoint{
				"web":       {Address: "[::]:80"},
				"webSecure": {Address: "[::]:443"},
			},
			Providers: prov,
		},
	}
	// Remote edge nodes poll the HTTP provider; expose the on-demand reload
	// endpoint so Miabi can make them pull immediately. The manager (local) uses a
	// watched file provider that already reloads on write, so it doesn't need it.
	if !s.usesFileProvider(srv) {
		cfg.Gateway.Reload = &reloadBlock{Enabled: true}
	}
	// Wire Redis for shared cache + distributed rate limiting when available: the
	// per-node Redis on a remote edge node, or the platform Redis on the manager.
	// The password is injected as ${GATEWAY_REDIS_PASSWORD} at deploy time.
	if addr := s.redisAddrFor(srv); addr != "" {
		cfg.Gateway.Redis = &redisBlock{Addr: addr, Password: "${" + gatewayRedisPasswordEnv + "}"}
	}
	if s.analyticsEnabledFor(srv) {
		cfg.Gateway.Analytics = &analyticsBlock{
			Enabled: true, Stream: s.analyticsStream,
			Sample: defaultAnalyticsSample, MaxLen: defaultAnalyticsMaxLen,
		}
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return ""
	}
	header := "# Miabi node gateway config. Edit from the node's detail page.\n"
	return header + string(body)
}

// Validate reports whether a config is parseable YAML (checked before saving an
// admin's edit).
func Validate(config string) error {
	if strings.TrimSpace(config) == "" {
		return ErrInvalidConfig
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(config), &out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return nil
}

// Ensure (re)deploys the node gateway on dc for srv, using token to authenticate to the control plane's
// HTTP provider. redisPassword is the per-node Redis password for remote edge nodes; empty on the
// manager, which reuses the platform Redis. Idempotent, so it is safe on every agent reconnect.
func (s *Service) Ensure(ctx context.Context, dc docker.Client, srv *models.Server, token, redisPassword string) error {
	if err := s.prepare(ctx, dc, srv, redisPassword); err != nil {
		return err
	}
	gw := s.Image(srv)
	if err := dc.PullImage(ctx, gw, nil); err != nil {
		return fmt.Errorf("pull gateway image %q: %w", gw, err)
	}
	_ = dc.RemoveContainer(ctx, ContainerName, true)
	if err := s.runGateway(ctx, dc, srv, token, redisPassword, ContainerName, gw, true); err != nil {
		return err
	}
	logger.Info("node gateway deployed", "node", srv.ID, "node_name", srv.Name, "image", gw)
	return nil
}

// SafeUpdate rolls the gateway to its resolved image without a hard cutover: since the gateway is the
// node's single ingress, it starts the new image as a test container, observes it for a grace period,
// and only then promotes it. The live gateway is untouched if the test never becomes healthy.
func (s *Service) SafeUpdate(ctx context.Context, dc docker.Client, srv *models.Server, token, redisPassword string, onPhase func(phase string, cause error)) error {
	gw := s.Image(srv)
	fail := func(err error) error {
		_ = dc.RemoveContainer(context.Background(), TestContainer, true)
		onPhase("failed", err)
		return err
	}

	onPhase("pulling", nil)
	if err := s.prepare(ctx, dc, srv, redisPassword); err != nil {
		return fail(err)
	}
	if err := dc.PullImage(ctx, gw, nil); err != nil {
		return fail(fmt.Errorf("pull gateway image %q: %w", gw, err))
	}

	// Start the new image as a throwaway test container (no published ports, so it
	// runs alongside the live gateway).
	onPhase("testing", nil)
	_ = dc.RemoveContainer(ctx, TestContainer, true)
	if err := s.runGateway(ctx, dc, srv, token, redisPassword, TestContainer, gw, false); err != nil {
		return fail(err)
	}

	// Observe: a bad image/config makes Goma exit on boot, so a container still
	// running after the grace window is a strong signal the update is safe.
	onPhase("observing", nil)
	select {
	case <-ctx.Done():
		return fail(ctx.Err())
	case <-time.After(updateObserveSeconds * time.Second):
	}
	cont, err := dc.InspectContainer(ctx, TestContainer)
	if err != nil {
		return fail(fmt.Errorf("inspect test container: %w", err))
	}
	if cont.State != "running" {
		return fail(fmt.Errorf("test container is not running (state %q, status %q) — new image/config rejected", cont.State, cont.Status))
	}

	// Promote: drop the test container, replace the live gateway with the new image.
	onPhase("promoting", nil)
	_ = dc.RemoveContainer(ctx, TestContainer, true)
	_ = dc.RemoveContainer(ctx, ContainerName, true)
	if err := s.runGateway(ctx, dc, srv, token, redisPassword, ContainerName, gw, true); err != nil {
		return fail(err)
	}

	onPhase("verifying", nil)
	if c, ierr := dc.InspectContainer(ctx, ContainerName); ierr != nil || c.State != "running" {
		return fail(fmt.Errorf("gateway did not come up after promotion"))
	}
	onPhase("done", nil)
	logger.Info("node gateway updated", "node", srv.ID, "node_name", srv.Name, "image", gw)
	return nil
}

// prepare resolves the node's config, ensures the shared network and (for remote
// edge nodes) the per-node Redis, and seeds the config volume with goma.yml.
// Shared by Ensure and SafeUpdate.
func (s *Service) prepare(ctx context.Context, dc docker.Client, srv *models.Server, redisPassword string) error {
	// Use the node's custom config when set, else the rendered default. A remote
	// node's default uses the HTTP provider and needs the control URL; the
	// manager's default uses the file provider and does not.
	cfg := strings.TrimSpace(srv.GatewayConfigYAML)
	if cfg == "" {
		if !s.usesFileProvider(srv) && s.controlURL == "" {
			return fmt.Errorf("control URL is not configured (set MIABI_CONTROL_URL)")
		}
		cfg = s.RenderConfig(srv)
	}

	// The gateway must share the node's app network to reach app containers by
	// their DNS alias.
	if _, err := dc.EnsureNetwork(ctx, s.network); err != nil {
		return fmt.Errorf("ensure network %q: %w", s.network, err)
	}

	// A remote edge node runs its own Redis (the manager reuses the platform one).
	if !isManager(srv) {
		if err := s.ensureNodeRedis(ctx, dc, srv, redisPassword); err != nil {
			return fmt.Errorf("ensure gateway redis: %w", err)
		}
	}

	// Seed the config volume with goma.yml via a short-lived helper container
	// (we drive the node's Docker over the tunnel and cannot write its host FS
	// directly). base64 in env avoids any shell-quoting of the YAML.
	helper := s.helperImg()
	if err := dc.PullImage(ctx, helper, nil); err != nil {
		return fmt.Errorf("pull helper image %q: %w", helper, err)
	}
	enc := base64.StdEncoding.EncodeToString([]byte(cfg))
	if code, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:   ContainerName + "-config",
		Image:  helper,
		Env:    []string{"GOMA_CFG_B64=" + enc},
		Cmd:    []string{"sh", "-c", "echo \"$GOMA_CFG_B64\" | base64 -d > " + configPath + "/goma.yml"},
		Mounts: map[string]string{ConfigVolume: configPath},
		Labels: s.labels(srv),
	}); err != nil {
		return fmt.Errorf("seed gateway config: %w", err)
	} else if code != 0 {
		return fmt.Errorf("seed gateway config: helper exited %d: %s", code, out)
	}
	return nil
}

// runGateway creates and starts a gateway container named name from image.
// publishPorts binds 80/443 (the live gateway); a test container omits them so
// it can run alongside the live one.
func (s *Service) runGateway(ctx context.Context, dc docker.Client, srv *models.Server, token, redisPassword, name, image string, publishPorts bool) error {
	mounts := map[string]string{
		ConfigVolume: configPath,
		CertsVolume:  "/etc/letsencrypt",
	}
	// Give the manager's gateway the very volume Miabi writes route files to — not a volume of the gateway's
	// own, which would leave the file provider watching a directory nothing ever writes to. Only mounted when
	// that volume is known; otherwise the config polls the HTTP provider and needs nothing.
	if s.usesFileProvider(srv) {
		mounts[s.providersVolume] = providersPath
	}
	spec := docker.RunSpec{
		Name:     name,
		Image:    image,
		Cmd:      []string{"server", "--config", configPath + "/goma.yml"},
		Env:      s.gatewayEnv(srv, token, redisPassword),
		Mounts:   mounts,
		Networks: []string{s.network},
		Labels:   s.labels(srv),
		// Goma answers /healthz on its HTTP port. Probed on the loopback INSIDE the container, so it reports "Goma is
		// serving" rather than "the host's :80 is reachable" — and so a SafeUpdate's test container, which publishes no
		// ports, is checked exactly like the live one. Until now the node's gateway had no health signal at all.
		Healthcheck: &docker.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "wget -qO- http://127.0.0.1/healthz >/dev/null 2>&1 || exit 1"},
			Interval:    10 * time.Second,
			Timeout:     5 * time.Second,
			Retries:     5,
			StartPeriod: 15 * time.Second,
		},
	}
	if publishPorts {
		spec.Ports = map[string]string{"80/tcp": "80", "443/tcp": "443"}
	}
	if _, err := dc.RunContainer(ctx, spec); err != nil {
		return fmt.Errorf("run gateway container %q: %w", name, err)
	}
	return nil
}

// gatewayEnv builds the gateway container's env: the provider token (referenced
// as ${INSTANCE_API_KEY}) and, when Redis is wired, its password (referenced as
// ${GATEWAY_REDIS_PASSWORD}). Neither secret lives in the editable config.
func (s *Service) gatewayEnv(srv *models.Server, token, redisPassword string) []string {
	env := []string{apiKeyEnv + "=" + token}
	// Protect the on-demand reload endpoint with the node's gateway token. Needed
	// by any gateway that polls the HTTP provider — every remote edge node, and a
	// manager gateway configured to poll instead of watching the volume.
	if !s.effectiveFileProvider(srv) {
		env = append(env, reloadTokenEnv+"="+token)
	}
	if s.redisAddrFor(srv) != "" {
		pw := redisPassword
		if isManager(srv) {
			pw = s.redisPassword
		}
		env = append(env, gatewayRedisPasswordEnv+"="+pw)
	}
	// When configured, hand the gateway the key it uses to encrypt sensitive
	// config (middleware rules + TLS) at rest.
	if s.configEncKey != "" {
		env = append(env, configEncryptionKeyEnv+"="+s.configEncKey)
	}
	return env
}

// ensureNodeRedis deploys the per-node gateway Redis (remote edge nodes) on the
// shared network, internal (no published ports) and password-protected. A no-op
// when it is already running, so cached data survives a gateway redeploy.
func (s *Service) ensureNodeRedis(ctx context.Context, dc docker.Client, srv *models.Server, password string) error {
	if cont, err := dc.InspectContainer(ctx, RedisContainer); err == nil && cont.State == "running" {
		return nil
	}
	img := s.redisImg()
	if err := dc.PullImage(ctx, img, nil); err != nil {
		return fmt.Errorf("pull redis image %q: %w", img, err)
	}
	// Cache-only Redis: no persistence (gateway state is rate-limit counters +
	// cache that can be safely rebuilt).
	cmd := []string{"redis-server", "--save", "", "--appendonly", "no"}
	if password != "" {
		cmd = append(cmd, "--requirepass", password)
	}
	labels := s.labels(srv)
	labels[docker.LabelRole] = docker.RoleNodeGatewayRedis
	_ = dc.RemoveContainer(ctx, RedisContainer, true)
	if _, err := dc.RunContainer(ctx, docker.RunSpec{
		Name:     RedisContainer,
		Image:    img,
		Cmd:      cmd,
		Networks: []string{s.network},
		Labels:   labels,
	}); err != nil {
		return fmt.Errorf("run redis container: %w", err)
	}
	return nil
}

// Teardown removes a node's gateway container, its test sibling, and the per-node
// Redis (best-effort; the config/cert volumes are left so ACME certs survive a
// re-add).
func (s *Service) Teardown(ctx context.Context, dc docker.Client) {
	_ = dc.RemoveContainer(ctx, ContainerName, true)
	_ = dc.RemoveContainer(ctx, TestContainer, true)
	_ = dc.RemoveContainer(ctx, RedisContainer, true)
}

// labels tag the gateway as platform infrastructure owned by the system
// workspace, so it is attributable in inventory, never treated as a user app, and
// never offered for import or stopped from the containers list.
func (s *Service) labels(srv *models.Server) map[string]string {
	extra := map[string]string{docker.LabelNode: srv.Name}
	if ws, err := s.workspaces.FindSystem(); err == nil {
		extra[docker.LabelWorkspace] = fmt.Sprintf("%d", ws.ID)
	}
	// managed-by=miabi: unlike the central gateway, Miabi provisions this one itself
	// and may freely recreate it (that is what SafeUpdate does).
	return docker.PlatformLabels(docker.RoleNodeGateway, docker.ManagedByMiabi, extra)
}
