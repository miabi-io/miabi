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

// Package edgegateway provisions the Goma gateway on an edge node, which terminates TLS and serves its own routes.
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
	"github.com/miabi-io/miabi/pkg/bundled"
	"github.com/miabi-io/miabi/pkg/stack/selfcontainer"
	"gopkg.in/yaml.v3"
)

// ErrInvalidConfig is returned when a submitted gateway config is not valid YAML.
var ErrInvalidConfig = errors.New("gateway config is not valid YAML")

const (
	// ContainerName is the gateway container's name on the node.
	ContainerName        = "mb-node-gateway"
	CentralContainerName = "miabi-gateway"
	CentralRoleValue     = "gateway"
	TestContainer        = ContainerName + "-test"
	// The manager reuses the platform Redis instead.
	RedisContainer = "mb-node-gateway-redis"
	// ConfigVolume holds the rendered goma.yml; CertsVolume persists ACME certs.
	ConfigVolume = "mb-node-gateway-config"
	CertsVolume  = "mb-node-gateway-certs"
	// helperImage is a tiny image used to seed the config volume with goma.yml.
	helperImage       = "busybox:1.36"
	redisImageDefault = bundled.RedisImage
	configPath        = "/etc/goma"
	// DefaultConfigFile is Goma Gateway's default config path inside the container.
	DefaultConfigFile = configPath + "/goma.yml"
	// ProvidersVolume is a legacy per-node providers volume.
	ProvidersVolume = "mb-node-gateway-providers"
	providersPath   = configPath + "/providers"
	// Referenced as ${...} so the stored config holds no secret.
	gatewayRedisPasswordEnv = "GATEWAY_REDIS_PASSWORD"
	updateObserveSeconds    = 25
)

// ConfigFilePath mirrors Goma's precedence: --config/-c flag, then GOMA_CONFIG_FILE, else /etc/goma/goma.yml.
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
	// Empty disables analytics in the rendered config.
	analyticsStream string
	// Empty means the manager's gateway polls the HTTP provider like a remote node.
	providersVolume string
	// attach joins a live gateway to networks beyond the node's app network, right after it starts.
	attach         GatewayAttacher
	agentContainer func(serverID uint) string
}

// GatewayAttacher runs on every start, since recreating the container drops extra network attachments.
type GatewayAttacher func(ctx context.Context, dc docker.Client, srv *models.Server, container string)

// SetGatewayAttacher wires the attachment made after each live gateway start. Optional.
func (s *Service) SetGatewayAttacher(fn GatewayAttacher) { s.attach = fn }

func NewService(workspaces *repositories.WorkspaceRepository, controlURL, image, network, acmeEmail string) *Service {
	return &Service{
		workspaces: workspaces,
		controlURL: strings.TrimRight(controlURL, "/"),
		image:      image,
		network:    network,
		acmeEmail:  acmeEmail,
	}
}

func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

func (s *Service) SetConfigEncryptionKey(key string) { s.configEncKey = strings.TrimSpace(key) }

// SetRedis wires the platform Redis the manager's gateway reuses; remote edge nodes run their own.
func (s *Service) SetRedis(addr, password string) {
	s.redisAddr = strings.TrimSpace(addr)
	s.redisPassword = password
}

// Matches the shipped stack's goma.yml so a dashboard-installed gateway behaves the same.
const (
	defaultAnalyticsSample = 1.0
	defaultAnalyticsMaxLen = 1_000_000
)

// SetAgentContainer lets the control plane attach the agent to the gateway network; nil leaves it as installed.
func (s *Service) SetAgentContainer(fn func(serverID uint) string) { s.agentContainer = fn }

func (s *Service) SetAnalytics(stream string) { s.analyticsStream = strings.TrimSpace(stream) }

func (s *Service) analyticsEnabledFor(srv *models.Server) bool {
	return s.analyticsStream != "" && s.redisAddrFor(srv) != ""
}

func (s *Service) redisImg() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyRedis); r != "" {
			return r
		}
	}
	return redisImageDefault
}

func (s *Service) RedisEnabled(srv *models.Server) bool { return s.redisAddrFor(srv) != "" }

// Keyed on where the gateway runs, not its route provider: a manager on HTTP still shares the platform Redis.
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

func (s *Service) Image(srv *models.Server) string {
	if srv != nil && strings.TrimSpace(srv.GatewayImage) != "" {
		return strings.TrimSpace(srv.GatewayImage)
	}
	return s.gatewayImage()
}

func ContainerNameFor(srv *models.Server) string {
	if srv != nil && strings.TrimSpace(srv.GatewayContainer) != "" {
		return strings.TrimSpace(srv.GatewayContainer)
	}
	return ContainerName
}

// AutoDeploy is false for imported gateways: a second mb-node-gateway on the same ingress ports would fail or duplicate.
func AutoDeploy(srv *models.Server) bool {
	return srv != nil && srv.Connectivity == models.ConnectivityEdgeGateway && !srv.GatewayImported
}

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

type gomaConfig struct {
	Version     int          `yaml:"version"`
	CertManager *certManager `yaml:"certManager,omitempty"`
	Gateway     gateway      `yaml:"gateway"`
}

// A route opts into a provider via tls.provider: <name>, or out of TLS with tls.provider: none.
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

// Events carry no client IP, only a daily-salted visitor hash; GOMA_ANALYTICS_* env overrides this block.
type analyticsBlock struct {
	Enabled bool   `yaml:"enabled"`
	Stream  string `yaml:"stream"`
	// Dashboards scale sampled counts back up, so lowering it trades precision for load.
	Sample float64 `yaml:"sample"`
	// Caps the stream so a stopped consumer cannot grow Redis without bound.
	MaxLen int `yaml:"maxLen"`
}

type logBlock struct {
	Level string `yaml:"level"`
}

// The token comes from GOMA_RELOAD_TOKEN so the stored config holds no secret.
type reloadBlock struct {
	Enabled bool `yaml:"enabled"`
}

// clearOnReload re-resolves backends whose IP changed on redeploy instead of serving a stale entry.
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

// Referenced as ${...} so the stored config holds no secret.
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

// The manager shares Miabi's providers volume, so written routes are picked up without HTTP polling.
type fileProvider struct {
	Enabled   bool   `yaml:"enabled"`
	Directory string `yaml:"directory"`
	Watch     bool   `yaml:"watch"`
}

// Referenced as ${...} so the stored config holds no secret.
const apiKeyEnv = "INSTANCE_API_KEY"

// Without it edge traffic is indistinguishable from the manager's.
const gatewayIDEnv = "GOMA_GATEWAY_ID"

// Set to ReloadToken(token), not the token itself, which would put the provider credential on the wire.
const reloadTokenEnv = "GOMA_RELOAD_TOKEN"

const configEncryptionKeyEnv = "GOMA_CONFIG_ENCRYPTION_KEY"

// Routes opt into another provider via tls.provider: <name>, or out via "none".
const defaultCertProvider = "acme"

const defaultLogLevel = "info"

// isManager decides infrastructure (shared Redis and providers volume), distinct from usesFileProvider.
func isManager(srv *models.Server) bool {
	return srv != nil && srv.IsLocal
}

// The file provider needs Miabi's own route directory, possible only on the manager with a known volume.
func (s *Service) usesFileProvider(srv *models.Server) bool {
	return isManager(srv) && s.providersVolume != ""
}

// SetProvidersVolume left empty (a manual install) makes the manager's gateway poll HTTP instead.
func (s *Service) SetProvidersVolume(name string) { s.providersVolume = strings.TrimSpace(name) }

// Only a named volume can be mounted into a second container, which is what tells a stack install from a manual one.
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
			// A bind mount can't be handed to another container by name, so the gateway polls instead.
			logger.Info("edgegateway: route directory is not a named volume; the manager's gateway will poll the HTTP provider",
				"destination", m.Destination, "type", m.Type)
			return ""
		}
		return m.Name
	}
	return ""
}

// A manager switched to the HTTP provider needs the same reload plumbing as a remote node.
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
	// The manager's watched file provider already reloads on write, so it doesn't need the endpoint.
	if !s.usesFileProvider(srv) {
		cfg.Gateway.Reload = &reloadBlock{Enabled: true}
	}
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

// Ensure is idempotent, so it is safe on every agent reconnect; redisPassword is empty on the manager.
func (s *Service) Ensure(ctx context.Context, dc docker.Client, srv *models.Server, token, redisPassword string) error {
	gw := s.Image(srv)
	cfg, err := s.resolvedConfig(srv)
	if err != nil {
		return err
	}
	// Before the short-circuit: agent network membership is not in the gateway spec, so unchanged gateways still need it.
	if !isManager(srv) {
		s.attachAgent(ctx, dc, srv)
	}
	want := hashSpec(s.gatewaySpec(srv, token, redisPassword, ContainerName, gw, cfg, true))
	if cur, ierr := dc.InspectContainer(ctx, ContainerName); ierr == nil &&
		cur.Labels[docker.LabelSpecHash] == want && cur.State == "running" {
		return nil
	}
	if err := s.prepareWith(ctx, dc, srv, redisPassword, cfg); err != nil {
		return err
	}
	if err := dc.PullImage(ctx, gw, nil); err != nil {
		return fmt.Errorf("pull gateway image %q: %w", gw, err)
	}
	_ = dc.RemoveContainer(ctx, ContainerName, true)
	if err := s.runGateway(ctx, dc, srv, token, redisPassword, ContainerName, gw, cfg, true); err != nil {
		return err
	}
	logger.Info("node gateway deployed", "node", srv.ID, "node_name", srv.Name, "image", gw)
	return nil
}

// SafeUpdate tests the new image in a throwaway container before promoting it; the live gateway is untouched on failure.
func (s *Service) SafeUpdate(ctx context.Context, dc docker.Client, srv *models.Server, token, redisPassword string, onPhase func(phase string, cause error)) error {
	gw := s.Image(srv)
	fail := func(err error) error {
		_ = dc.RemoveContainer(context.Background(), TestContainer, true)
		onPhase("failed", err)
		return err
	}

	onPhase("pulling", nil)
	cfg, cerr := s.resolvedConfig(srv)
	if cerr != nil {
		return fail(cerr)
	}
	if err := s.prepareWith(ctx, dc, srv, redisPassword, cfg); err != nil {
		return fail(err)
	}
	if err := dc.PullImage(ctx, gw, nil); err != nil {
		return fail(fmt.Errorf("pull gateway image %q: %w", gw, err))
	}

	// No published ports, so it runs alongside the live gateway.
	onPhase("testing", nil)
	_ = dc.RemoveContainer(ctx, TestContainer, true)
	if err := s.runGateway(ctx, dc, srv, token, redisPassword, TestContainer, gw, cfg, false); err != nil {
		return fail(err)
	}

	// A bad image or config makes Goma exit on boot, so surviving the grace window signals a safe update.
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
	if err := s.runGateway(ctx, dc, srv, token, redisPassword, ContainerName, gw, cfg, true); err != nil {
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

// A remote node's default config needs the control URL; the manager's file-provider default does not.
func (s *Service) resolvedConfig(srv *models.Server) (string, error) {
	if cfg := strings.TrimSpace(srv.GatewayConfigYAML); cfg != "" {
		return cfg, nil
	}
	if !s.usesFileProvider(srv) && s.controlURL == "" {
		return "", fmt.Errorf("control URL is not configured (set MIABI_CONTROL_URL)")
	}
	return s.RenderConfig(srv), nil
}

func (s *Service) prepareWith(ctx context.Context, dc docker.Client, srv *models.Server, redisPassword, cfg string) error {
	if _, err := dc.EnsureNetwork(ctx, s.network); err != nil {
		return fmt.Errorf("ensure network %q: %w", s.network, err)
	}

	// A remote edge node runs its own Redis (the manager reuses the platform one).
	if !isManager(srv) {
		if err := s.ensureNodeRedis(ctx, dc, srv, redisPassword); err != nil {
			return fmt.Errorf("ensure gateway redis: %w", err)
		}
		s.attachAgent(ctx, dc, srv)
	}

	// The node's Docker is driven over the tunnel, so its host FS can't be written; base64 avoids YAML shell-quoting.
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

// attachAgent puts the node's agent container on the gateway network.
func (s *Service) attachAgent(ctx context.Context, dc docker.Client, srv *models.Server) {
	if s.agentContainer == nil {
		return
	}
	id := s.agentContainer(srv.ID)
	if id == "" {
		return
	}
	if err := dc.NetworkConnect(ctx, s.network, id, nil); err != nil {
		logger.Warn("could not attach the node agent to the gateway network; analytics from this node will not be forwarded",
			"node", srv.Name, "network", s.network, "container", id, "error", err)
		return
	}
	logger.Info("node agent attached to the gateway network", "node", srv.Name, "network", s.network)
}

// The fingerprint lets Ensure tell an unchanged gateway from one to recreate; test containers publish no ports.
func (s *Service) runGateway(ctx context.Context, dc docker.Client, srv *models.Server, token, redisPassword, name, image, cfg string, publishPorts bool) error {
	spec := s.gatewaySpec(srv, token, redisPassword, name, image, cfg, publishPorts)
	spec.Labels[docker.LabelSpecHash] = hashSpec(spec)
	if _, err := dc.RunContainer(ctx, spec); err != nil {
		return fmt.Errorf("run gateway container %q: %w", name, err)
	}
	if publishPorts && s.attach != nil {
		s.attach(ctx, dc, srv, name)
	}
	return nil
}

// The config lives in a volume, so without its fingerprint a config edit would look like no change.
func (s *Service) gatewaySpec(srv *models.Server, token, redisPassword, name, image, cfg string, publishPorts bool) docker.RunSpec {
	mounts := map[string]string{
		ConfigVolume: configPath,
		CertsVolume:  "/etc/letsencrypt",
	}
	// Must be Miabi's own route volume, or the file provider would watch a directory nothing writes to.
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
		// Probed on loopback inside the container so a SafeUpdate test container without ports is checked the same way.
		Healthcheck: &docker.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "wget -qO- http://127.0.0.1/healthz >/dev/null 2>&1 || exit 1"},
			Interval:    10 * time.Second,
			Timeout:     5 * time.Second,
			Retries:     5,
			StartPeriod: 15 * time.Second,
		},
	}
	spec.Labels[configHashLabel] = configFingerprint(cfg)
	if publishPorts {
		spec.Ports = map[string]string{"80/tcp": "80", "443/tcp": "443"}
	}
	return spec
}

// Secrets go in env so neither lives in the editable config.
func (s *Service) gatewayEnv(srv *models.Server, token, redisPassword string) []string {
	env := []string{apiKeyEnv + "=" + token}
	// Lets the manager tell which node served a request once edge events share the stream.
	if s.analyticsEnabledFor(srv) && srv != nil && srv.Name != "" {
		env = append(env, gatewayIDEnv+"="+srv.Name)
	}
	// Needed by any gateway that polls the HTTP provider, including a manager configured to poll.
	if !s.effectiveFileProvider(srv) {
		env = append(env, reloadTokenEnv+"="+ReloadToken(token))
	}
	if s.redisAddrFor(srv) != "" {
		pw := redisPassword
		if isManager(srv) {
			pw = s.redisPassword
		}
		env = append(env, gatewayRedisPasswordEnv+"="+pw)
	}
	// Must match the key the provider endpoint renders this node's bundle under; both derive it from the same inputs.
	if k := ConfigKey(srv, s.configEncKey); k != "" {
		env = append(env, configEncryptionKeyEnv+"="+k)
	}
	return env
}

// Keeps a same-spec Redis so cache survives; a password change (reinstalled node) forces a recreate.
func (s *Service) ensureNodeRedis(ctx context.Context, dc docker.Client, srv *models.Server, password string) error {
	// No persistence: rate-limit counters and cache can be safely rebuilt.
	cmd := []string{"redis-server", "--save", "", "--appendonly", "no"}
	if password != "" {
		cmd = append(cmd, "--requirepass", password)
	}
	labels := s.labels(srv)
	labels[docker.LabelRole] = docker.RoleNodeGatewayRedis
	spec := docker.RunSpec{
		Name:     RedisContainer,
		Image:    s.redisImg(),
		Cmd:      cmd,
		Networks: []string{s.network},
		Labels:   labels,
	}
	want := hashSpec(spec)
	if cont, err := dc.InspectContainer(ctx, RedisContainer); err == nil && cont.State == "running" &&
		cont.Labels[docker.LabelSpecHash] == want {
		return nil
	}
	spec.Labels[docker.LabelSpecHash] = want
	if err := dc.PullImage(ctx, spec.Image, nil); err != nil {
		return fmt.Errorf("pull redis image %q: %w", spec.Image, err)
	}
	_ = dc.RemoveContainer(ctx, RedisContainer, true)
	if _, err := dc.RunContainer(ctx, spec); err != nil {
		return fmt.Errorf("run redis container: %w", err)
	}
	return nil
}

// Teardown leaves the config and cert volumes so ACME certs survive a re-add.
func (s *Service) Teardown(ctx context.Context, dc docker.Client) {
	_ = dc.RemoveContainer(ctx, ContainerName, true)
	_ = dc.RemoveContainer(ctx, TestContainer, true)
	_ = dc.RemoveContainer(ctx, RedisContainer, true)
}

// Platform labels keep the gateway out of user apps, import offers and the containers list's stop action.
func (s *Service) labels(srv *models.Server) map[string]string {
	extra := map[string]string{docker.LabelNode: srv.Name}
	if s.workspaces != nil {
		if ws, err := s.workspaces.FindSystem(); err == nil {
			extra[docker.LabelWorkspace] = fmt.Sprintf("%d", ws.ID)
		}
	}
	// Unlike the central gateway, Miabi provisions this one and may freely recreate it.
	return docker.PlatformLabels(docker.RoleNodeGateway, docker.ManagedByMiabi, extra)
}
