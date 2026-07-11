// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	goutils "github.com/jkaninda/go-utils"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/joho/godotenv"
	errorhandlers "github.com/miabi-io/miabi/internal/error_handlers"
	"github.com/miabi-io/miabi/internal/storage"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Config holds all runtime configuration, loaded from the environment.
type Config struct {
	Database DatabaseConfig
	Redis    RedisConfig

	Env     string
	Port    int
	DevMode bool

	JWTSecret     string
	EncryptionKey string

	AdminEmail    string
	AdminPassword string

	OpenAPIDocs    bool
	MetricsEnabled bool
	UpdateCheck    bool
	// PlanEnforcement gates per-workspace resource quotas. On by default; set it
	// false and all plan limits and capabilities become unlimited/allowed.
	PlanEnforcement bool
	// SecurityEnforcement guards platform-managed resources from disruptive raw
	// Docker actions in the admin dashboard. When true (default), a platform admin
	// cannot stop or remove a Miabi-managed container (apps, databases, …) from the
	// node containers list — those are managed through their owning resource.
	// Setting it false is an escape hatch for break-glass operations.
	SecurityEnforcement bool

	// GPU management. GPUEnabled is the master off-switch: when false the control
	// plane never probes nodes for GPUs and the UI hides all GPU controls (an
	// air-gapped / no-GPU fleet pays nothing). NvidiaRuntime is the container
	// runtime name that signals the NVIDIA Container Toolkit is present.
	// GPUProbeImage is the one-shot image the inventory probe runs `nvidia-smi -q
	// -x` in (point it at a mirror for air-gapped/registry-pinned fleets).
	// GPUInventoryMinutes is the device rescan interval.
	GPUEnabled          bool
	NvidiaRuntime       string
	GPUProbeImage       string
	GPUInventoryMinutes int

	// LicensePublicKey is the base64 Ed25519 public key used to verify a
	// commercial license offline. In Enterprise builds a key is normally baked
	// into the binary; this env override is for dev/test. Empty in Community.
	LicensePublicKey string
	// LicenseFile, when set, is a path to a signed license token auto-installed on
	// boot (air-gapped / IaC friendly: drop the file and restart). A newer
	// DB-installed license still takes precedence.
	LicenseFile string
	// Metrics history scraper.
	MetricsScrapeSeconds  int
	MetricsRetentionHours int

	CORSOrigins string
	AppWebURL   string
	ApiBaseURL  string
	// AppName is the product name shown in platform notification emails.
	AppName string
	// SystemSMTP is the SMTP server Miabi uses to send its own platform
	// notification emails (password resets, workspace invitations, welcomes).
	// When unset (no host/from), those emails are silently skipped.
	SystemSMTP SystemSMTPConfig
	// WebDir overrides where the web UI is served from. The UI is normally
	// embedded in the binary (internal/web); setting MIABI_WEB_DIR serves it from
	// this directory on disk instead — for frontend development (live rebuilds) or
	// to swap in a custom build without recompiling. Empty ⇒ use the embedded UI.
	WebDir string

	// AllowDowngrade lets the server boot even when the binary's version is
	// older than the version recorded in the database. Off by default.
	AllowDowngrade bool

	// WebhookAllowPrivateTargets permits outbound webhooks to RFC1918/ULA
	// private addresses (homelab/LAN targets). Loopback and link-local
	// (incl. cloud metadata) are always blocked regardless. Off by default.
	WebhookAllowPrivateTargets bool

	// Worker settings.
	WorkerConcurrency int
	WorkerMaxRetries  int

	// DockerHost is the Docker engine endpoint (informational; the SDK reads
	// DOCKER_HOST from the environment via FromEnv).
	DockerHost string

	// MarketplaceURL is where Miabi syncs official + community templates from.
	// The default is the marketplace repo's latest GitHub Release asset; that path
	// always serves the newest published (immutable) release, so the catalog stays
	// fresh without flapping like a floating branch ref would, and updates decouple
	// from Miabi releases.
	//
	// Alternatives: any static bundle URL (a CDN-served export.json, used
	// directly) or a marketplace server base URL, e.g. https://marketplace.miabi.io
	// (the /v1/export path is appended). Set MIABI_MARKETPLACE_URL to an explicit
	// empty value to disable syncing — the catalog falls back to the embedded
	// official floor only (offline / air-gapped kill switch).
	MarketplaceURL string

	// GomaProviderDir is Goma Gateway's watched file-provider directory. When
	// set, Miabi writes route files there; otherwise an in-memory proxy is
	// used (dev). TLS/ACME is configured on Goma itself.
	GomaProviderDir string

	// DeletionGraceDays is how many days an admin-scheduled account deletion waits
	// before the account and all its data are permanently purged. Default 7.
	DeletionGraceDays int

	// DNSReconcileMinutes is the cadence of the managed-DNS reconcile sweep, which
	// re-asserts ledgered records and refreshes provider status. Default 30.
	DNSReconcileMinutes int

	// StorageUsageEnabled runs the sweep that measures real volume disk usage
	// (docker system df) and caches it. Default on; off shows declared sizes only.
	StorageUsageEnabled bool
	// StorageUsageMinutes is that sweep's cadence. Default 30.
	StorageUsageMinutes int

	// ACMEDirectoryURL is the ACME CA directory Miabi issues managed (DNS-01)
	// certificates from. Empty = Let's Encrypt production. Point it at the LE
	// staging directory for testing so issuance never burns prod rate limits.
	ACMEDirectoryURL string
	// CertRenewDays is how many days before expiry a managed certificate is
	// auto-renewed. Default 30.
	CertRenewDays int

	// KeyAutoRotate enables the per-workspace encryption-key auto-rotation cron;
	// KeyRotateMonths is how old an active key may get before it is rotated
	// (re-encrypting the workspace's secrets). Off by default.
	KeyAutoRotate   bool
	KeyRotateMonths int

	// HostProcPath is the procfs directory used to read real host CPU/memory for
	// the local node. Defaults to /host/proc (the convention when the host's
	// procfs is bound read-only); if that isn't present the local /proc is used,
	// which already reflects host stats. Host metrics are simply unavailable when
	// neither is readable.
	HostProcPath string

	// ProxyNetwork is the shared Docker network that Goma Gateway and all
	// managed app containers join so the proxy can reach app backends. Goma must
	// also be attached to this network.
	ProxyNetwork string

	// ControlURL is the public base URL remote nodes reach the control plane at
	// (e.g. https://miabi.example.com). Used to point a node's own Goma Gateway
	// at the HTTP-provider endpoint. Falls back to ApiBaseURL when unset.
	ControlURL string
	// ExternalBaseDomain is the wildcard base domain for one-click external access
	// (e.g. "apps.example.com", DNS *.apps.example.com). When set it is the
	// authoritative value for the `external_base_domain` platform setting, applied
	// on every boot; leave empty to manage it from the admin Settings UI instead.
	ExternalBaseDomain string
	// ExternalBaseProvider names the Goma certManager provider used for the
	// generated external-access routes' certificates ("" = the gateway default).
	// Authoritative for the `external_base_provider` setting when set, like
	// ExternalBaseDomain.
	ExternalBaseProvider string
	// NodeGatewayImage is the Goma Gateway image deployed on edge-gateway nodes.
	NodeGatewayImage string
	// RunnerImage is the miabi-runner image shown in the runner enrollment command.
	RunnerImage string
	// AcmeEmail is the contact address a node gateway's ACME (Let's Encrypt)
	// certificate manager registers with.
	AcmeEmail string
	// GomaConfigEncryptionKey, when set, is injected into every edge gateway as
	// GOMA_CONFIG_ENCRYPTION_KEY so Goma encrypts sensitive parts of its config
	// (middleware rules and TLS material) at rest. Empty = no config encryption.
	GomaConfigEncryptionKey string

	// Registry is the built-in Docker registry config. When any field is set it is
	// authoritative on boot (overriding the admin Settings UI), mirroring the
	// ExternalBaseDomain convention; otherwise manage it from Settings.
	Registry RegistryConfig

	// LogStore is the shared store for execution logs (deployments, pipeline
	// steps, jobs). Filesystem-backed by default; see plans/log-storage.md.
	LogStore LogStoreConfig

	// HostPortMin/HostPortMax bound the host ports an admin may approve for port
	// bindings. Defaults allow the full non-privileged range.
	HostPortMin int
	HostPortMax int

	// Database port-forward (on-demand external access to managed databases).
	// ForwardBindAddr is the interface the ephemeral forward listeners bind to
	// (default 127.0.0.1 — admins set it to a reachable interface to allow remote
	// DB clients). ForwardAdvertiseHost is the host shown to users in the
	// connection string (falls back to the bind address). ForwardRelayImage is
	// the socat image run as the in-network relay. ForwardTTLMinutes is how long
	// a session stays open before it is reaped.
	ForwardBindAddr      string
	ForwardAdvertiseHost string
	ForwardRelayImage    string
	ForwardTTLMinutes    int

	// RestoreMaxMB caps the size of an uploaded database dump for restore.
	RestoreMaxMB int

	// Container security profile (OpenShift-style "run as non-root"). When a
	// workspace's plan selects the "restricted" profile — or ForceNonRootUser is
	// set globally — application and job containers are started as RestrictedUID:0
	// with no-new-privileges and NET_RAW dropped. RestrictedUID is the platform
	// non-root UID (GID is always 0, the arbitrary-UID convention). SecurityInitImage
	// is the tiny image run to chown a restricted app's managed volumes to that UID.
	RestrictedUID     int
	ForceNonRootUser  bool
	SecurityInitImage string

	// Managed network subnet allocation. Miabi carves a unique subnet out of
	// NetworkPoolCIDR for every Docker network it creates (workspace/user/stack/
	// gateway/overlay) and passes it as explicit IPAM, instead of relying on
	// Docker's small built-in default-address-pools (which exhaust with the error
	// "all predefined address pools have been fully subnetted"). It follows a
	// cluster-CIDR → per-network /24 scheme. NetworkSubnetPrefix is the size of each
	// carved subnet; the default /12 pool split into /24s yields 4096 networks.
	// The default base (10.64.0.0/12) is chosen to avoid common Kubernetes CNI, Docker,
	// and LAN/VPN/Tailscale ranges.
	NetworkPoolCIDR     string
	NetworkSubnetPrefix int

	// Runners: dedicated build/pipeline machines that keep build load off the app
	// hosting nodes (see plans/runners.md). Every build runs on a registered
	// runner — there is no in-process/on-node fallback. Register a runner from
	// Settings → Runners (or an admin-managed shared runner).
	//
	// RunnerWaitTimeout bounds how long a build waits for an available runner
	// before it fails ("no runner became available"). While waiting the run stays
	// pending and is re-checked periodically. Prevents runs piling up forever when
	// no runner is ever registered.
	RunnerWaitTimeout time.Duration
	// JobAPITokenEnabled injects the scoped MIABI_JOB_TOKEN callback credential
	// into runner jobs (report status/logs, deploy this app by digest). A
	// hardened install can withhold it while still injecting registry
	// credentials. Default true.
	JobAPITokenEnabled bool
	// BuildTimeoutMinutes is the hard per-build deadline the runner dispatcher
	// enforces on a build job (0 = none).
	BuildTimeoutMinutes int

	securitySchemes okapi.SecuritySchemes
}

// DatabaseConfig holds PostgreSQL connection settings and the live GORM handle.
type DatabaseConfig struct {
	DB       *gorm.DB
	host     string
	user     string
	password string
	name     string
	port     int
	sslMode  string
	url      string
}

// SystemSMTPConfig is the SMTP server Miabi uses to send its own platform
// notification emails. Encryption is one of: none, starttls, ssl.
type SystemSMTPConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	Encryption string
}

// IsConfigured reports whether enough is set to send (a host and a From address).
func (s SystemSMTPConfig) IsConfigured() bool {
	return s.Host != "" && s.From != ""
}

// PostgresConn resolves the control-plane database connection parameters,
// parsing MIABI_DB_URL when set and otherwise using the discrete MIABI_DB_*
// fields. Used by the platform backup runner to point pg-bkup at Miabi's own
// database. sslmode defaults to "disable" when not otherwise specified.
func (d DatabaseConfig) PostgresConn() (host string, port int, name, user, password, sslmode string) {
	host, port, name, user, password, sslmode = d.host, d.port, d.name, d.user, d.password, d.sslMode
	if d.url == "" {
		return host, port, name, user, password, sslmode
	}
	u, err := url.Parse(d.url)
	if err != nil || u.Host == "" {
		return host, port, name, user, password, sslmode
	}
	if h := u.Hostname(); h != "" {
		host = h
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	if db := strings.TrimPrefix(u.Path, "/"); db != "" {
		name = db
	}
	if u.User != nil {
		if un := u.User.Username(); un != "" {
			user = un
		}
		if pw, ok := u.User.Password(); ok {
			password = pw
		}
	}
	if m := u.Query().Get("sslmode"); m != "" {
		sslmode = m
	}
	return host, port, name, user, password, sslmode
}

// RedisConfig holds Redis connection settings and the live client.
type RedisConfig struct {
	Client   *redis.Client
	Addr     string
	Password string
}

// RegistryConfig is the boot-authoritative config for the built-in registry. A
// set field overrides the corresponding admin setting on boot.
type RegistryConfig struct {
	Enabled     bool
	Host        string
	StorageType string // "filesystem" | "s3"
	Image       string // override the registry image (also via the image catalog)
	// AuthURL is the address the gateway's forwardAuth calls to reach Miabi's
	// /internal/registry/auth (e.g. http://miabi:9000). Falls back to ControlURL.
	AuthURL string
	// PlatformToken is an OPTIONAL override for the shared secret the build/deploy
	// worker uses to push and pull built images to/from the registry (the
	// "platform-minted credential"). Leave it empty: the platform derives the token
	// internally from the master encryption key, so distribution works with no
	// operator action. Set it only to pin a specific value (e.g. to share with
	// external tooling). Authorize accepts it as a platform principal for any
	// namespace.
	PlatformToken string
	S3Endpoint    string
	S3Bucket      string
	S3Region      string
	S3AccessKey   string
	S3SecretKey   string
	S3ForcePath   bool
}

// LogStoreConfig configures the shared execution-log store. The filesystem
// backend (default) writes gzip log objects under Dir, which must be a shared
// mount across the control plane and every worker that reads/writes it. Backend
// "off" keeps today's DB-tail-only behavior (no store).
type LogStoreConfig struct {
	Backend       string // "filesystem" | "off"
	Dir           string // shared directory for the filesystem backend
	RetentionDays int    // retention window for the sweeper (0 = keep forever)
	MaxBytes      int64  // per-log cap; middle-truncated past it (0 = default)
	TailBytes     int    // bounded DB tail size
	Compression   string // "gzip" | "none"
}

// Enabled reports whether logs should be externalized to the store.
func (l LogStoreConfig) Enabled() bool {
	return l.Backend != "" && l.Backend != "off"
}

// IsSet reports whether any registry env var was provided, i.e. env is the
// authoritative source for the registry config on this boot.
func (r RegistryConfig) IsSet() bool {
	return r.Enabled || r.Host != "" || r.StorageType != "" || r.Image != "" ||
		r.S3Endpoint != "" || r.S3Bucket != "" || r.S3Region != "" ||
		r.S3AccessKey != "" || r.S3SecretKey != ""
}

// New loads configuration from the environment (and an optional .env file).
func New() *Config {
	if err := godotenv.Load(); err != nil {
		logger.Debug("no .env file found, using environment variables")
	}
	return &Config{
		Database: DatabaseConfig{
			host:     goutils.Env("MIABI_DB_HOST", "localhost"),
			user:     goutils.Env("MIABI_DB_USER", "miabi"),
			password: goutils.Env("MIABI_DB_PASSWORD", "miabi"),
			name:     goutils.Env("MIABI_DB_NAME", "miabi"),
			port:     goutils.EnvInt("MIABI_DB_PORT", 5432),
			sslMode:  goutils.Env("MIABI_DB_SSL_MODE", "disable"),
			url:      goutils.Env("MIABI_DB_URL", ""),
		},
		Redis: RedisConfig{
			Addr:     goutils.Env("MIABI_REDIS_ADDR", "localhost:6379"),
			Password: goutils.Env("MIABI_REDIS_PASSWORD", ""),
		},
		Env:                   goutils.Env("MIABI_ENV", "dev"),
		Port:                  goutils.EnvInt("MIABI_PORT", 9000),
		DevMode:               goutils.EnvBool("MIABI_DEV_MODE", false),
		JWTSecret:             goutils.Env("MIABI_JWT_SECRET", defaultJWTSecret),
		EncryptionKey:         goutils.Env("MIABI_ENCRYPTION_KEY", ""),
		AdminEmail:            goutils.Env("MIABI_ADMIN_EMAIL", "admin@example.com"),
		AdminPassword:         goutils.Env("MIABI_ADMIN_PASSWORD", defaultAdminPassword),
		OpenAPIDocs:           goutils.EnvBool("MIABI_OPENAPI_DOCS", true),
		UpdateCheck:           goutils.EnvBool("MIABI_UPDATE_CHECK", true),
		MetricsEnabled:        goutils.EnvBool("MIABI_METRICS_ENABLED", false),
		PlanEnforcement:       goutils.EnvBool("MIABI_PLAN_ENFORCEMENT", true),
		SecurityEnforcement:   goutils.EnvBool("MIABI_SECURITY_ENFORCEMENT", true),
		GPUEnabled:            goutils.EnvBool("MIABI_GPU_ENABLED", false),
		NvidiaRuntime:         goutils.Env("MIABI_NVIDIA_RUNTIME", "nvidia"),
		GPUProbeImage:         goutils.Env("MIABI_GPU_PROBE_IMAGE", "nvidia/cuda:12.4.1-base-ubuntu22.04"),
		GPUInventoryMinutes:   goutils.EnvInt("MIABI_GPU_INVENTORY_MINUTES", 30),
		LicensePublicKey:      goutils.Env("MIABI_LICENSE_PUBLIC_KEY", ""),
		LicenseFile:           goutils.Env("MIABI_LICENSE_FILE", ""),
		MetricsScrapeSeconds:  goutils.EnvInt("MIABI_METRICS_SCRAPE_SECONDS", 60),
		MetricsRetentionHours: goutils.EnvInt("MIABI_METRICS_RETENTION_HOURS", 24),
		CORSOrigins:           goutils.Env("MIABI_CORS_ORIGINS", "*"),
		AppWebURL:             goutils.Env("MIABI_WEB_URL", ""),
		ApiBaseURL:            goutils.Env("MIABI_API_URL", ""),
		AppName:               goutils.Env("MIABI_APP_NAME", "Miabi"),
		SystemSMTP: SystemSMTPConfig{
			Host:       goutils.Env("MIABI_SMTP_HOST", ""),
			Port:       goutils.EnvInt("MIABI_SMTP_PORT", 587),
			Username:   goutils.Env("MIABI_SMTP_USERNAME", ""),
			Password:   goutils.Env("MIABI_SMTP_PASSWORD", ""),
			From:       goutils.Env("MIABI_SMTP_FROM", ""),
			Encryption: goutils.Env("MIABI_SMTP_ENCRYPTION", "starttls"),
		},
		WebDir:                     goutils.Env("MIABI_WEB_DIR", ""),
		AllowDowngrade:             goutils.EnvBool("MIABI_ALLOW_DOWNGRADE", false),
		WebhookAllowPrivateTargets: goutils.EnvBool("MIABI_WEBHOOK_ALLOW_PRIVATE_TARGETS", false),
		WorkerConcurrency:          goutils.EnvInt("MIABI_WORKER_CONCURRENCY", 10),
		WorkerMaxRetries:           goutils.EnvInt("MIABI_WORKER_MAX_RETRIES", 5),
		DockerHost:                 goutils.Env("DOCKER_HOST", "unix:///var/run/docker.sock"),
		MarketplaceURL:             goutils.Env("MIABI_MARKETPLACE_URL", marketplaceURL),
		GomaProviderDir:            goutils.Env("MIABI_GOMA_PROVIDER_DIR", "/etc/goma/providers"),
		HostProcPath:               goutils.Env("MIABI_HOST_PROC", "/host/proc"),
		DeletionGraceDays:          goutils.EnvInt("MIABI_DELETION_GRACE_DAYS", 7),
		DNSReconcileMinutes:        goutils.EnvInt("MIABI_DNS_RECONCILE_MINUTES", 30),
		StorageUsageEnabled:        goutils.EnvBool("MIABI_STORAGE_USAGE_ENABLED", true),
		StorageUsageMinutes:        goutils.EnvInt("MIABI_STORAGE_USAGE_MINUTES", 60),
		ACMEDirectoryURL:           goutils.Env("MIABI_ACME_DIRECTORY_URL", ""),
		CertRenewDays:              goutils.EnvInt("MIABI_CERT_RENEW_DAYS", 30),
		KeyAutoRotate:              goutils.EnvBool("MIABI_KEY_AUTO_ROTATE", false),
		KeyRotateMonths:            goutils.EnvInt("MIABI_KEY_ROTATE_MONTHS", 6),
		ProxyNetwork:               goutils.Env("MIABI_PROXY_NETWORK", goutils.Env("MIABI_GOMA_NETWORK", "miabi")),
		ControlURL:                 goutils.Env("MIABI_CONTROL_URL", goutils.Env("MIABI_API_URL", "")),
		ExternalBaseDomain:         goutils.Env("MIABI_EXTERNAL_BASE_DOMAIN", ""),
		ExternalBaseProvider:       goutils.Env("MIABI_EXTERNAL_BASE_PROVIDER", ""),
		NodeGatewayImage:           goutils.Env("MIABI_NODE_GATEWAY_IMAGE", "jkaninda/goma-gateway:latest"),
		RunnerImage:                goutils.Env("MIABI_RUNNER_IMAGE", "miabi/runner:latest"),
		GomaConfigEncryptionKey:    goutils.Env("GOMA_CONFIG_ENCRYPTION_KEY", ""),
		Registry: RegistryConfig{
			Enabled:       goutils.EnvBool("MIABI_REGISTRY_ENABLED", false),
			Host:          goutils.Env("MIABI_REGISTRY_HOST", ""),
			StorageType:   goutils.Env("MIABI_REGISTRY_STORAGE", ""),
			Image:         goutils.Env("MIABI_REGISTRY_IMAGE", ""),
			AuthURL:       goutils.Env("MIABI_REGISTRY_AUTH_URL", "http://miabi:9000"),
			PlatformToken: goutils.Env("MIABI_REGISTRY_PLATFORM_TOKEN", ""),
			S3Endpoint:    goutils.Env("MIABI_REGISTRY_S3_ENDPOINT", ""),
			S3Bucket:      goutils.Env("MIABI_REGISTRY_S3_BUCKET", ""),
			S3Region:      goutils.Env("MIABI_REGISTRY_S3_REGION", ""),
			S3AccessKey:   goutils.Env("MIABI_REGISTRY_S3_ACCESS_KEY", ""),
			S3SecretKey:   goutils.Env("MIABI_REGISTRY_S3_SECRET_KEY", ""),
			S3ForcePath:   goutils.EnvBool("MIABI_REGISTRY_S3_FORCE_PATH_STYLE", false),
		},
		LogStore: LogStoreConfig{
			Backend:       goutils.Env("MIABI_LOG_BACKEND", "filesystem"),
			Dir:           goutils.Env("MIABI_LOG_DIR", miabiLogDir),
			RetentionDays: goutils.EnvInt("MIABI_LOG_RETENTION_DAYS", 30),
			MaxBytes:      int64(goutils.EnvInt("MIABI_LOG_MAX_BYTES", 32<<20)),
			TailBytes:     goutils.EnvInt("MIABI_LOG_TAIL_BYTES", 16<<10),
			Compression:   goutils.Env("MIABI_LOG_COMPRESSION", "gzip"),
		},
		AcmeEmail:            goutils.Env("MIABI_ACME_EMAIL", ""),
		HostPortMin:          goutils.EnvInt("MIABI_HOST_PORT_MIN", 1024),
		HostPortMax:          goutils.EnvInt("MIABI_HOST_PORT_MAX", 65535),
		ForwardBindAddr:      goutils.Env("MIABI_FORWARD_BIND_ADDR", "127.0.0.1"),
		ForwardAdvertiseHost: goutils.Env("MIABI_FORWARD_ADVERTISE_HOST", ""),
		ForwardRelayImage:    goutils.Env("MIABI_FORWARD_RELAY_IMAGE", "alpine/socat:latest"),
		ForwardTTLMinutes:    goutils.EnvInt("MIABI_FORWARD_TTL_MINUTES", 30),
		RestoreMaxMB:         goutils.EnvInt("MIABI_RESTORE_MAX_MB", 1024),
		RestrictedUID:        goutils.EnvInt("MIABI_RESTRICTED_UID", 100000),
		ForceNonRootUser:     goutils.EnvBool("MIABI_FORCE_NON_ROOT_USER", false),
		SecurityInitImage:    goutils.Env("MIABI_SECURITY_INIT_IMAGE", "busybox:latest"),
		NetworkPoolCIDR:      goutils.Env("MIABI_NETWORK_POOL_CIDR", "10.64.0.0/12"),
		NetworkSubnetPrefix:  goutils.EnvInt("MIABI_NETWORK_SUBNET_PREFIX", 24),
		BuildTimeoutMinutes:  goutils.EnvInt("MIABI_BUILD_TIMEOUT_MINUTES", 30),
		RunnerWaitTimeout:    time.Duration(goutils.EnvInt("MIABI_RUNNER_WAIT_TIMEOUT_MINUTES", 30)) * time.Minute,
		JobAPITokenEnabled:   goutils.EnvBool("MIABI_JOB_API_TOKEN_ENABLED", true),
		securitySchemes:      okapi.SecuritySchemes{},
	}
}

// Insecure development defaults that ship working for local dev but must be
// overridden in production; validate() refuses to boot in non-dev if any remain.
const (
	defaultJWTSecret     = "change-me-in-production"
	defaultAdminPassword = "admin@1234"
)

func (c *Config) validate() error {
	// Network pool sanity always runs (dev included) so a misconfiguration fails
	// fast at boot rather than at the first network create.
	if err := c.validateNetworkPool(); err != nil {
		return err
	}
	if c.Env == "dev" {
		return nil
	}
	// Non-dev: refuse to start with insecure defaults. Each of these is a real
	// security boundary (session signing, secret encryption, the seeded admin,
	// and CORS) that must not run on a shipped default.
	if c.JWTSecret == "" || c.JWTSecret == defaultJWTSecret {
		return fmt.Errorf("MIABI_JWT_SECRET must be set to a non-default value in non-dev environments")
	}
	if strings.TrimSpace(c.EncryptionKey) == "" {
		return fmt.Errorf("MIABI_ENCRYPTION_KEY must be set in non-dev environments; without it, secrets are stored unencrypted at rest")
	}
	if c.AdminPassword == "" || c.AdminPassword == defaultAdminPassword {
		return fmt.Errorf("MIABI_ADMIN_PASSWORD must be set to a non-default value in non-dev environments")
	}
	if hasWildcardOrigin(c.CORSOrigins) {
		return fmt.Errorf(`MIABI_CORS_ORIGINS must be an explicit allowlist (not "*") in non-dev environments; a wildcard origin is unsafe with credentialed requests`)
	}
	return nil
}

// validateNetworkPool checks the managed-network subnet pool: the CIDR must parse
// and the per-network prefix must be larger than the pool prefix (so at least one
// subnet fits) and small enough to leave usable host addresses.
func (c *Config) validateNetworkPool() error {
	// Empty = allocator disabled (Load always sets a default; only a hand-built
	// Config, e.g. in tests, is empty). Managed networks then fall back to Docker.
	if strings.TrimSpace(c.NetworkPoolCIDR) == "" {
		return nil
	}
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(c.NetworkPoolCIDR))
	if err != nil {
		return fmt.Errorf("MIABI_NETWORK_POOL_CIDR %q is not a valid CIDR: %w", c.NetworkPoolCIDR, err)
	}
	poolPrefix, _ := ipnet.Mask.Size()
	if c.NetworkSubnetPrefix <= poolPrefix {
		return fmt.Errorf("MIABI_NETWORK_SUBNET_PREFIX (%d) must be larger than the pool prefix (/%d) so subnets fit inside MIABI_NETWORK_POOL_CIDR", c.NetworkSubnetPrefix, poolPrefix)
	}
	if c.NetworkSubnetPrefix > 30 {
		return fmt.Errorf("MIABI_NETWORK_SUBNET_PREFIX (%d) leaves no usable host addresses; use /30 or larger", c.NetworkSubnetPrefix)
	}
	return nil
}

// AllowedBrowserOrigins returns the browser origins permitted for same-origin
// checks (e.g. WebSocket upgrades): the configured CORS allowlist plus the web
// UI URL. A "*" entry (dev) means no restriction.
func (c *Config) AllowedBrowserOrigins() []string {
	out := make([]string, 0, 4)
	for _, o := range strings.Split(c.CORSOrigins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	if w := strings.TrimSpace(c.AppWebURL); w != "" {
		out = append(out, w)
	}
	return out
}

// hasWildcardOrigin reports whether the comma-separated origin list contains a
// "*" wildcard entry.
func hasWildcardOrigin(origins string) bool {
	for _, o := range strings.Split(origins, ",") {
		if strings.TrimSpace(o) == "*" {
			return true
		}
	}
	return false
}

// DeploymentURL is this instance's public URL, used to bind a URL-scoped license
// (matched by host). Prefers the web URL, falling back to the API URL; empty
// when neither is configured, which disables the license URL check.
func (c *Config) DeploymentURL() string {
	if strings.TrimSpace(c.AppWebURL) != "" {
		return c.AppWebURL
	}
	return c.ApiBaseURL
}

func (c *Config) initLogger() *logger.Logger {
	if c.DevMode {
		return logger.New(logger.WithDebugLevel())
	}
	return logger.New(logger.WithJSONFormat(), logger.WithInfoLevel())
}

// Initialize validates config and applies it to the Okapi app (logger, CORS,
// OpenAPI docs, error handler). Storage is connected separately in InitStorage.
func (c *Config) Initialize(app *okapi.Okapi) error {
	if err := c.validate(); err != nil {
		return err
	}

	l := c.initLogger()
	if c.DevMode {
		app.WithDebug()
	}
	app.WithPort(c.Port)
	app.WithLogger(l.Logger)
	_ = goutils.SetEnv("ENV", c.Env)

	// Only reachable in dev without an encryption key (validate() blocks this in
	// non-dev); make the downgrade to unencrypted-at-rest impossible to miss.
	if strings.TrimSpace(c.EncryptionKey) == "" {
		logger.Warn("MIABI_ENCRYPTION_KEY is not set — secrets are stored WITHOUT encryption at rest (permitted only in dev)")
	}

	corsOrigins := strings.Split(c.CORSOrigins, ",")
	for i := range corsOrigins {
		corsOrigins[i] = strings.TrimSpace(corsOrigins[i])
	}
	// A wildcard origin is incompatible with credentialed CORS: browsers forbid
	// "*" + credentials, and reflecting the request origin would let any site make
	// authenticated calls. validate() already rejects "*" in non-dev; in dev we
	// keep the wildcard working but drop credentials to avoid the unsafe combo.
	app.WithCORS(okapi.Cors{
		AllowedOrigins:   corsOrigins,
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Request-ID", "X-Agent-Version"},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowCredentials: !hasWildcardOrigin(c.CORSOrigins),
	})

	if c.OpenAPIDocs {
		c.securitySchemes = append(c.securitySchemes, okapi.SecurityScheme{
			Name:         "BearerAuth",
			Description:  "Bearer token issued by /auth/login. Send as: `Authorization: Bearer <JWT>`.",
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "JWT",
		})
		c.securitySchemes = append(c.securitySchemes, okapi.SecurityScheme{
			Name:         "ApiKeyAuth",
			Description:  "Long-lived API key. Send as: `Authorization: Bearer <API_KEY>`.",
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "Miabi API Key",
		})

		apiServers := okapi.Servers{}
		if c.AppWebURL != "" {
			apiServers = append(apiServers, okapi.Server{URL: c.AppWebURL})
		}
		if c.ApiBaseURL != "" {
			apiServers = append(apiServers, okapi.Server{URL: c.ApiBaseURL})
		}

		app.WithOpenAPIDocs(okapi.OpenAPI{
			Title:       "Miabi API",
			Version:     Version,
			Description: "The open-source, self-hosted Platform-as-a-Service (PaaS) for Docker.",
			License: okapi.License{
				Name: "AGPL-3.0-or-later",
				URL:  "https://www.gnu.org/licenses/agpl-3.0.html",
			},
			Servers:         apiServers,
			SecuritySchemes: c.securitySchemes,
			Favicon:         "/favicon.svg",
			UI:              okapi.ScalarUI,
		})
	}

	app.WithErrorHandler(errorhandlers.CustomErrorHandler())
	return nil
}

// InitWorker prepares configuration for the worker process.
func (c *Config) InitWorker() error {
	c.initLogger()
	return c.validate()
}

// InitStorage connects to PostgreSQL and Redis. Fatal on failure.
func (c *Config) InitStorage() {
	var dsn string
	if c.Database.url != "" {
		dsn = c.Database.url
	} else {
		dsn = fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=%s",
			c.Database.host, c.Database.user, c.Database.password,
			c.Database.name, c.Database.port, c.Database.sslMode)
	}

	db, err := storage.ConnectPostgres(dsn)
	if err != nil {
		logger.Fatal("failed to connect to database", "error", err)
	}
	c.Database.DB = db

	rdb, err := storage.NewRedis(c.Redis.Addr, c.Redis.Password)
	if err != nil {
		logger.Fatal("failed to connect to redis", "error", err)
	}
	c.Redis.Client = rdb
}
