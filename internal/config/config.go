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

package config

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	goutils "github.com/jkaninda/go-utils"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/joho/godotenv"
	errorhandlers "github.com/miabi-io/miabi/internal/error_handlers"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Config holds all runtime configuration, loaded from the environment.
type Config struct {
	Database DatabaseConfig
	Redis    RedisConfig

	Env     string
	Port    int
	DevMode bool

	// LogLevel is MIABI_LOG_LEVEL (debug|info|warn|error|off); empty means debug in dev, info otherwise.
	LogLevel string

	JWTSecret     string
	EncryptionKey string

	AdminEmail           string
	AdminPassword        string
	PasswordResetEnabled bool

	OpenAPIDocs bool
	// MigrationEnabled opens nothing by itself: a workspace is exported only while its owner has a migration started.
	MigrationEnabled     bool
	BillingAPIEnabled    bool
	BillingHardAfterDays int
	// BillingConnectionLocked keeps the console connection from being removed from inside Miabi.
	BillingConnectionLocked bool
	MetricsEnabled          bool
	UpdateCheck             bool
	// PlanEnforcement false makes all plan limits and capabilities unlimited.
	PlanEnforcement bool
	// SecurityEnforcement stops admins from stopping or removing Miabi-managed containers; false is break-glass.
	SecurityEnforcement bool

	// GPUProbeImage runs nvidia-smi; point it at a mirror when air-gapped.
	GPUEnabled          bool
	NvidiaRuntime       string
	GPUProbeImage       string
	GPUInventoryMinutes int

	// LicenseFile is a signed license token auto-installed on boot; a newer DB-installed license still wins.
	LicenseFile           string
	MetricsScrapeSeconds  int
	MetricsRetentionHours int

	CORSOrigins string
	// TrustedProxies may set X-Forwarded-For and X-Real-IP; empty trusts those headers from any peer.
	TrustedProxies []string
	AppWebURL      string
	ApiBaseURL     string
	// PasskeysEnabled off refuses passkey sign-in but keeps stored passkeys, so re-enabling restores them.
	PasskeysEnabled bool
	// Passkeys are off without a public URL (MIABI_WEB_URL or MIABI_API_URL), since a passkey is bound to an origin.
	PasskeyRPID    string
	PasskeyOrigins []string
	// LoginTokenMaxTTLHours is the hard ceiling a caller may request for a CLI login token.
	LoginTokenTTLHours    int
	LoginTokenMaxTTLHours int
	// AppName is the product name shown in platform notification emails.
	AppName string
	// SystemSMTP sends platform emails; when unset (no host/from) those emails are silently skipped.
	SystemSMTP SystemSMTPConfig
	// WebDir (MIABI_WEB_DIR) serves the web UI from disk instead of the embedded build; empty uses the embedded UI.
	WebDir string

	// AllowDowngrade lets the server boot when the binary is older than the version recorded in the database.
	AllowDowngrade bool

	// WebhookAllowPrivateTargets permits RFC1918/ULA webhook targets; loopback and link-local are always blocked.
	WebhookAllowPrivateTargets bool

	WorkerConcurrency int
	WorkerMaxRetries  int
	// WorkerHealthEnabled off lets a worker run beside the server on one host without both binding MIABI_PORT.
	WorkerHealthEnabled bool

	// DockerHost is informational; the SDK reads DOCKER_HOST from the environment.
	DockerHost string

	// MarketplaceURL ending in .json is fetched directly, else /v1/export is appended; empty disables syncing.
	MarketplaceURL string

	// GomaProviderDir is Goma's watched route directory; when unset an in-memory proxy is used (dev).
	GomaProviderDir string

	DeletionGraceDays int

	DNSReconcileMinutes int

	// StorageUsageEnabled off shows declared volume sizes only.
	StorageUsageEnabled bool
	// StorageUsageMinutes is that sweep's cadence. Default 30.
	StorageUsageMinutes int

	DatabaseSizeCron string

	AnalyticsEnabled bool
	// AnalyticsStream must match the gateway's GOMA_ANALYTICS_STREAM.
	AnalyticsStream string
	// AnalyticsFlushSeconds is how often the consumer persists closed buckets.
	AnalyticsFlushSeconds int
	// AnalyticsRetentionDays bounds how long rollups are kept before pruning.
	AnalyticsRetentionDays int
	// AnalyticsLiveWindowSeconds must match the gateway's monitoring.visitorTTL or the live counts disagree.
	AnalyticsLiveWindowSeconds int

	// ACMEDirectoryURL empty means Let's Encrypt production; use staging for tests to spare prod rate limits.
	ACMEDirectoryURL string
	CertRenewDays    int

	// SecurityPolicies false ignores stored Security Center policies without deleting them.
	SecurityPolicies bool
	// AdminUnlock is superseded by the Enterprise admin_access policy.
	AdminUnlock bool
	// KeyRotateMonths is how old an active key may get before auto-rotation re-encrypts the workspace's secrets.
	KeyAutoRotate   bool
	KeyRotateMonths int

	// HostProcPath falls back to the local /proc when absent; host metrics are unavailable if neither is readable.
	HostProcPath string
	// NodeStatsAgent off measures every remote node with a helper container instead.
	NodeStatsAgent bool
	// MetadataGuard blocks containers from the cloud metadata service via nftables; off removes the table.
	MetadataGuard bool

	// ProxyNetwork is empty until startup resolves it with node.ResolveAppNetwork when MIABI_PROXY_NETWORK is unset.
	ProxyNetwork string

	// InternalNetwork is empty on a Docker Compose install, leaving ProxyNetwork as the only fabric.
	InternalNetwork string

	// APIKeyScopeEnforcement empty means enforce, except on upgrades with keys in use (see middlewares.ResolveScopeMode).
	APIKeyScopeEnforcement string

	// ControlURL is where remote nodes reach the control plane; falls back to ApiBaseURL when unset.
	ControlURL string
	// RegistrationEnabled is fixed at boot: opening sign-up lets strangers create accounts, so it belongs to the deployment.
	RegistrationEnabled bool
	// Empty when the variable is unset; a set value pins the setting and is re-applied on every boot.
	RequireEmailVerification string
	AllowedSignupDomains     string
	// Pinned the same way: MIABI_WORKSPACE_CREATION and MIABI_INVITATION_SIGNUP_ENABLED.
	WorkspaceCreation       string
	InvitationSignupEnabled string

	// ControlManagerMode empty leaves the mode to Platform Settings.
	ControlManagerMode string

	// Telemetry is environment only; a truthy DO_NOT_TRACK also turns it off.
	Telemetry bool

	// ExternalBaseDomain, when set, pins the default cluster's external domain on every boot.
	ExternalBaseDomain string
	// ExternalBaseProvider "" means the gateway default; when set it pins that field like ExternalBaseDomain.
	ExternalBaseProvider string
	// NodeGatewayImage is the Goma Gateway image deployed on edge-gateway nodes.
	NodeGatewayImage string
	// RunnerImage is the miabi-runner image shown in the runner enrollment command.
	RunnerImage string
	AcmeEmail   string
	// GomaConfigEncryptionKey makes Goma encrypt middleware rules and TLS material at rest; empty disables it.
	GomaConfigEncryptionKey string

	// Registry, when any field is set, overrides the admin Settings UI on boot.
	Registry RegistryConfig

	PlatformBackup PlatformBackupConfig

	LogStore LogStoreConfig

	ContainerGrantsEnabled bool

	// Privileged workspaces may request any host port outside HostPortMin/HostPortMax.
	HostPortMin int
	HostPortMax int

	// ForwardTTLMinutes is how long a port-forward session stays open before it is reaped.
	ForwardBindAddr      string
	ForwardAdvertiseHost string
	ForwardRelayImage    string
	ForwardTTLMinutes    int

	// RestoreMaxMB caps the size of an uploaded database dump for restore.
	RestoreMaxMB int

	// SecurityInitImage chowns volumes for the restricted RestrictedUID:0 profile.
	RestrictedUID     int
	ForceNonRootUser  bool
	SecurityInitImage string

	// QuotaCPUOvercommit is safe because a CPU limit is a ceiling, not a reservation; memory is never overcommitted.
	QuotaCPUOvercommit float64

	// NetworkSubnetPrefix sizes each subnet carved from NetworkPoolCIDR; /12 split into /24s yields 4096 networks.
	NetworkPoolCIDR     string
	NetworkSubnetPrefix int
	// NetworkIPv6 is always set explicitly so networks never inherit the daemon default; needs Docker Engine 26+.
	NetworkIPv6 bool
	// NetworkIPv6ULAPrefix derives each /64 from the IPv4 subnet, giving stable addresses and working on Engine 25.
	NetworkIPv6ULAPrefix string

	RunnerWaitTimeout time.Duration
	// JobAPITokenEnabled false withholds MIABI_JOB_TOKEN while still injecting registry credentials.
	JobAPITokenEnabled bool
	// BuildTimeoutMinutes 0 means no deadline.
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

// SystemSMTPConfig Encryption is one of none, starttls, ssl.
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

// PostgresConn prefers MIABI_DB_URL over the discrete fields; sslmode defaults to "disable".
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
	DB       int // Redis database index (MIABI_REDIS_DB); must match Goma's GOMA_REDIS_DB for analytics
}

// PlatformBackupConfig fields set here win over the stored settings row and are reported as env-locked.
type PlatformBackupConfig struct {
	S3Endpoint       string
	S3Bucket         string
	S3Region         string
	S3AccessKey      string
	S3SecretKey      string
	S3UseSSL         bool
	S3ForcePathStyle bool

	RootPath     string
	DatabasePath string
	VolumePath   string

	// Passphrase lets an unattended install take encrypted backups from first boot; it is never returned by the API.
	Passphrase string
	Encrypt    bool
	// EncryptSet lets an explicit "false" override a stored "true" while an absent variable leaves it alone.
	EncryptSet bool

	// IncludeIdentity defaults on once a passphrase is set, since that makes a recovery point restorable on a fresh host.
	IncludeIdentity    bool
	IncludeIdentitySet bool
	// IncludeTenantData adds every workspace's databases and volumes.
	IncludeTenantData    bool
	IncludeTenantDataSet bool

	ScheduleCron  string
	MaxBackups    int
	RetentionDays int
}

// Configured reports whether the environment supplies a usable S3 target.
func (c PlatformBackupConfig) Configured() bool {
	return c.S3Bucket != "" && c.S3AccessKey != "" && c.S3SecretKey != ""
}

// RegistryConfig enablement, hostname and storage have no other source; the admin API accepts none of them.
type RegistryConfig struct {
	// EnabledSet distinguishes an explicit false from an absent variable, which leaves the switch to the admin UI.
	Enabled    bool
	EnabledSet bool
	Host       string
	// StorageType "s3" requires the registry_s3 entitlement, checked both at selection and at container start.
	StorageType string
	// StorageClass places the filesystem driver's volume on a registered storage class.
	StorageClass string
	Image        string // override the registry image (also via the image catalog)
	// AuthURL falls back to ControlURL.
	AuthURL string
	// PlatformToken is normally derived from the master encryption key; set it only to pin a value.
	PlatformToken string
	S3Endpoint    string
	S3Bucket      string
	S3Region      string
	S3AccessKey   string
	S3SecretKey   string
	S3ForcePath   bool
	// HTTPSRedirect must be off behind a TLS terminator without proxy.trustedProxies, or registry requests loop.
	HTTPSRedirect bool
}

// LogStoreConfig Dir must be a shared mount across the control plane and every worker.
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

// Lowercase-only: Docker lowercases image hosts, so a case-folded match would fail the ownership prefix check.
var registryHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

// The message names no variable: the host may be derived from the external base domain.
var ErrRegistryHostInvalid = errors.New("invalid registry host")

// NormalizeRegistryHost is a security boundary: a host with a scheme, path or wildcard is inert.
func NormalizeRegistryHost(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	if h == "" {
		return "", nil
	}
	if strings.Contains(h, "://") {
		return "", fmt.Errorf("%w %q: drop the scheme, a registry host is a bare hostname (registry.example.com)", ErrRegistryHostInvalid, raw)
	}
	if strings.ContainsAny(h, "/?#") {
		return "", fmt.Errorf("%w %q: a registry host carries no path", ErrRegistryHostInvalid, raw)
	}
	if !registryHostPattern.MatchString(h) {
		return "", fmt.Errorf("%w %q: expected a DNS hostname with an optional port (registry.example.com[:5000])", ErrRegistryHostInvalid, raw)
	}
	// A single-label host is indistinguishable from a Docker Hub namespace.
	if !strings.ContainsAny(h, ".:") && !strings.HasPrefix(h, "localhost") {
		return "", fmt.Errorf("%w %q: use a dotted hostname or add a port — Docker reads a single-label host as a Docker Hub namespace", ErrRegistryHostInvalid, raw)
	}
	return h, nil
}

// Refuses to boot in dev too: an unmatched host looks healthy until the pull ownership check.
func (c *Config) validateRegistry() error {
	if _, err := NormalizeRegistryHost(c.Registry.Host); err != nil {
		return fmt.Errorf("MIABI_REGISTRY_HOST: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(c.Registry.StorageType)) {
	case "", models.RegistryStorageFilesystem, models.RegistryStorageS3:
	default:
		return fmt.Errorf("MIABI_REGISTRY_STORAGE %q: expected %q or %q",
			c.Registry.StorageType, models.RegistryStorageFilesystem, models.RegistryStorageS3)
	}
	if sc := strings.TrimSpace(c.Registry.StorageClass); sc != "" && !models.ValidStorageClassName(sc) {
		return fmt.Errorf("MIABI_REGISTRY_STORAGE_CLASS %q: %w", sc, models.ErrStorageClassName)
	}
	return nil
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
			DB:       goutils.EnvInt("MIABI_REDIS_DB", 0),
		},
		Env:                     goutils.Env("MIABI_ENV", "production"),
		Port:                    goutils.EnvInt("MIABI_PORT", 9000),
		DevMode:                 goutils.EnvBool("MIABI_DEV_MODE", false),
		LogLevel:                goutils.Env("MIABI_LOG_LEVEL", "info"),
		JWTSecret:               goutils.Env("MIABI_JWT_SECRET", defaultJWTSecret),
		EncryptionKey:           goutils.Env("MIABI_ENCRYPTION_KEY", ""),
		AdminEmail:              goutils.Env("MIABI_ADMIN_EMAIL", "admin@example.com"),
		AdminPassword:           goutils.Env("MIABI_ADMIN_PASSWORD", defaultAdminPassword),
		PasswordResetEnabled:    goutils.EnvBool("MIABI_PASSWORD_RESET_ENABLED", true),
		OpenAPIDocs:             goutils.EnvBool("MIABI_OPENAPI_DOCS", true),
		MigrationEnabled:        goutils.EnvBool("MIABI_MIGRATION_ENABLED", false),
		BillingAPIEnabled:       goutils.EnvBool("MIABI_BILLING_API_ENABLED", false),
		BillingHardAfterDays:    goutils.EnvInt("MIABI_BILLING_HARD_AFTER_DAYS", 14),
		BillingConnectionLocked: goutils.EnvBool("MIABI_BILLING_CONNECTION_LOCKED", false),
		UpdateCheck:             goutils.EnvBool("MIABI_UPDATE_CHECK", true),
		MetricsEnabled:          goutils.EnvBool("MIABI_METRICS_ENABLED", false),
		PlanEnforcement:         goutils.EnvBool("MIABI_PLAN_ENFORCEMENT", true),
		SecurityEnforcement:     goutils.EnvBool("MIABI_SECURITY_ENFORCEMENT", true),
		GPUEnabled:              goutils.EnvBool("MIABI_GPU_ENABLED", false),
		NvidiaRuntime:           goutils.Env("MIABI_NVIDIA_RUNTIME", "nvidia"),
		GPUProbeImage:           goutils.Env("MIABI_GPU_PROBE_IMAGE", "nvidia/cuda:12.4.1-base-ubuntu22.04"),
		GPUInventoryMinutes:     goutils.EnvInt("MIABI_GPU_INVENTORY_MINUTES", 30),
		LicenseFile:             goutils.Env("MIABI_LICENSE_FILE", ""),
		MetricsScrapeSeconds:    goutils.EnvInt("MIABI_METRICS_SCRAPE_SECONDS", 60),
		MetricsRetentionHours:   goutils.EnvInt("MIABI_METRICS_RETENTION_HOURS", 24),
		CORSOrigins:             goutils.Env("MIABI_CORS_ORIGINS", goutils.Env("MIABI_WEB_URL", "*")),
		TrustedProxies:          splitList(goutils.Env("MIABI_TRUSTED_PROXIES", "")),
		AppWebURL:               goutils.Env("MIABI_WEB_URL", ""),
		ApiBaseURL:              goutils.Env("MIABI_API_URL", ""),
		PasskeysEnabled:         goutils.EnvBool("MIABI_PASSKEYS_ENABLED", true),
		PasskeyRPID:             goutils.Env("MIABI_PASSKEY_RP_ID", ""),
		PasskeyOrigins:          splitList(goutils.Env("MIABI_PASSKEY_ORIGINS", "")),
		LoginTokenTTLHours:      goutils.EnvInt("MIABI_LOGIN_TOKEN_TTL_HOURS", 24),
		LoginTokenMaxTTLHours:   goutils.EnvInt("MIABI_LOGIN_TOKEN_MAX_TTL_HOURS", 168),
		AppName:                 goutils.Env("MIABI_APP_NAME", "Miabi"),
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
		WorkerHealthEnabled:        goutils.EnvBool("MIABI_WORKER_HEALTH_ENABLED", true),
		DockerHost:                 goutils.Env("DOCKER_HOST", "unix:///var/run/docker.sock"),
		MarketplaceURL:             goutils.Env("MIABI_MARKETPLACE_URL", marketplaceURL),
		GomaProviderDir:            goutils.Env("MIABI_GOMA_PROVIDER_DIR", "/etc/goma/providers"),
		HostProcPath:               goutils.Env("MIABI_HOST_PROC", "/host/proc"),
		NodeStatsAgent:             goutils.EnvBool("MIABI_NODE_STATS_AGENT", true),
		MetadataGuard:              goutils.EnvBool("MIABI_METADATA_GUARD", false),
		DeletionGraceDays:          goutils.EnvInt("MIABI_DELETION_GRACE_DAYS", 7),
		DNSReconcileMinutes:        goutils.EnvInt("MIABI_DNS_RECONCILE_MINUTES", 30),
		StorageUsageEnabled:        goutils.EnvBool("MIABI_STORAGE_USAGE_ENABLED", true),
		StorageUsageMinutes:        goutils.EnvInt("MIABI_STORAGE_USAGE_MINUTES", 60),
		DatabaseSizeCron:           goutils.Env("MIABI_DATABASE_SIZE_CRON", "20 2 * * *"),
		AnalyticsEnabled:           goutils.EnvBool("MIABI_ANALYTICS_ENABLED", true),
		AnalyticsStream:            goutils.Env("MIABI_ANALYTICS_STREAM", "goma:analytics"),
		AnalyticsFlushSeconds:      goutils.EnvInt("MIABI_ANALYTICS_FLUSH_SECONDS", 15),
		AnalyticsRetentionDays:     goutils.EnvInt("MIABI_ANALYTICS_RETENTION_DAYS", 90),
		AnalyticsLiveWindowSeconds: goutils.EnvInt("MIABI_ANALYTICS_LIVE_WINDOW_SECONDS", 300),
		ACMEDirectoryURL:           goutils.Env("MIABI_ACME_DIRECTORY_URL", ""),
		CertRenewDays:              goutils.EnvInt("MIABI_CERT_RENEW_DAYS", 30),
		KeyAutoRotate:              goutils.EnvBool("MIABI_KEY_AUTO_ROTATE", false),
		SecurityPolicies:           goutils.EnvBool("MIABI_SECURITY_POLICIES", true),
		AdminUnlock:                goutils.EnvBool("MIABI_ADMIN_UNLOCK", false),
		KeyRotateMonths:            goutils.EnvInt("MIABI_KEY_ROTATE_MONTHS", 6),
		ProxyNetwork:               goutils.Env("MIABI_PROXY_NETWORK", goutils.Env("MIABI_GOMA_NETWORK", "miabi-proxy")),
		InternalNetwork:            goutils.Env("MIABI_INTERNAL_NETWORK", "miabi-internal"),
		APIKeyScopeEnforcement:     goutils.Env("MIABI_API_KEY_SCOPE_ENFORCEMENT", ""),
		ControlURL:                 goutils.Env("MIABI_CONTROL_URL", goutils.Env("MIABI_API_URL", "")),
		RegistrationEnabled:        goutils.EnvBool("MIABI_REGISTRATION_ENABLED", false),
		RequireEmailVerification:   goutils.Env("MIABI_REQUIRE_EMAIL_VERIFICATION", ""),
		AllowedSignupDomains:       goutils.Env("MIABI_ALLOWED_SIGNUP_DOMAINS", ""),
		WorkspaceCreation:          goutils.Env("MIABI_WORKSPACE_CREATION", ""),
		InvitationSignupEnabled:    goutils.Env("MIABI_INVITATION_SIGNUP_ENABLED", ""),
		ControlManagerMode:         goutils.Env("MIABI_CONTROL_MANAGER_MODE", ""),
		Telemetry:                  goutils.EnvBool("MIABI_TELEMETRY", true) && !doNotTrack(goutils.Env("DO_NOT_TRACK", "")),
		ExternalBaseDomain:         goutils.Env("MIABI_EXTERNAL_BASE_DOMAIN", ""),
		ExternalBaseProvider:       goutils.Env("MIABI_EXTERNAL_BASE_PROVIDER", ""),
		NodeGatewayImage:           goutils.Env("MIABI_NODE_GATEWAY_IMAGE", DefaultGomaImage),
		RunnerImage:                goutils.Env("MIABI_RUNNER_IMAGE", "miabi/runner:latest"),
		GomaConfigEncryptionKey:    goutils.Env("GOMA_CONFIG_ENCRYPTION_KEY", ""),
		PlatformBackup: PlatformBackupConfig{
			S3Endpoint:       goutils.Env("MIABI_PLATFORM_BACKUP_S3_ENDPOINT", ""),
			S3Bucket:         goutils.Env("MIABI_PLATFORM_BACKUP_S3_BUCKET", ""),
			S3Region:         goutils.Env("MIABI_PLATFORM_BACKUP_S3_REGION", ""),
			S3AccessKey:      goutils.Env("MIABI_PLATFORM_BACKUP_S3_ACCESS_KEY", ""),
			S3SecretKey:      goutils.Env("MIABI_PLATFORM_BACKUP_S3_SECRET_KEY", ""),
			S3UseSSL:         goutils.EnvBool("MIABI_PLATFORM_BACKUP_S3_USE_SSL", true),
			S3ForcePathStyle: goutils.EnvBool("MIABI_PLATFORM_BACKUP_S3_FORCE_PATH_STYLE", false),

			RootPath:     goutils.Env("MIABI_PLATFORM_BACKUP_PATH", ""),
			DatabasePath: goutils.Env("MIABI_PLATFORM_BACKUP_DATABASE_PATH", ""),
			VolumePath:   goutils.Env("MIABI_PLATFORM_BACKUP_VOLUME_PATH", ""),

			Passphrase: goutils.Env("MIABI_PLATFORM_BACKUP_PASSPHRASE", ""),
			Encrypt:    goutils.EnvBool("MIABI_PLATFORM_BACKUP_ENCRYPT", true),
			EncryptSet: os.Getenv("MIABI_PLATFORM_BACKUP_ENCRYPT") != "",

			IncludeIdentity:      goutils.EnvBool("MIABI_PLATFORM_BACKUP_INCLUDE_IDENTITY", true),
			IncludeIdentitySet:   os.Getenv("MIABI_PLATFORM_BACKUP_INCLUDE_IDENTITY") != "",
			IncludeTenantData:    goutils.EnvBool("MIABI_PLATFORM_BACKUP_INCLUDE_TENANT_DATA", false),
			IncludeTenantDataSet: os.Getenv("MIABI_PLATFORM_BACKUP_INCLUDE_TENANT_DATA") != "",

			ScheduleCron:  goutils.Env("MIABI_PLATFORM_BACKUP_SCHEDULE", ""),
			MaxBackups:    goutils.EnvInt("MIABI_PLATFORM_BACKUP_MAX", 0),
			RetentionDays: goutils.EnvInt("MIABI_PLATFORM_BACKUP_RETENTION_DAYS", 0),
		},
		Registry: RegistryConfig{
			Enabled:       goutils.EnvBool("MIABI_REGISTRY_ENABLED", false),
			EnabledSet:    envPresent("MIABI_REGISTRY_ENABLED"),
			Host:          goutils.Env("MIABI_REGISTRY_HOST", ""),
			HTTPSRedirect: goutils.EnvBool("MIABI_REGISTRY_HTTPS_REDIRECT", true),
			StorageType:   goutils.Env("MIABI_REGISTRY_STORAGE", ""),
			StorageClass:  goutils.Env("MIABI_REGISTRY_STORAGE_CLASS", ""),
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
		AcmeEmail:              goutils.Env("MIABI_ACME_EMAIL", ""),
		ContainerGrantsEnabled: goutils.EnvBool("MIABI_CONTAINER_GRANTS_ENABLED", false),
		HostPortMin:            goutils.EnvInt("MIABI_HOST_PORT_MIN", 1024),
		HostPortMax:            goutils.EnvInt("MIABI_HOST_PORT_MAX", 65535),
		ForwardBindAddr:        goutils.Env("MIABI_FORWARD_BIND_ADDR", "127.0.0.1"),
		ForwardAdvertiseHost:   goutils.Env("MIABI_FORWARD_ADVERTISE_HOST", ""),
		ForwardRelayImage:      goutils.Env("MIABI_FORWARD_RELAY_IMAGE", "alpine/socat:latest"),
		ForwardTTLMinutes:      goutils.EnvInt("MIABI_FORWARD_TTL_MINUTES", 30),
		RestoreMaxMB:           goutils.EnvInt("MIABI_RESTORE_MAX_MB", 1024),
		RestrictedUID:          goutils.EnvInt("MIABI_RESTRICTED_UID", 100000),
		ForceNonRootUser:       goutils.EnvBool("MIABI_FORCE_NON_ROOT_USER", false),
		QuotaCPUOvercommit:     envRatio("MIABI_QUOTA_CPU_OVERCOMMIT"),
		SecurityInitImage:      goutils.Env("MIABI_SECURITY_INIT_IMAGE", "busybox:latest"),
		NetworkPoolCIDR:        goutils.Env("MIABI_NETWORK_POOL_CIDR", "10.64.0.0/12"),
		NetworkSubnetPrefix:    goutils.EnvInt("MIABI_NETWORK_SUBNET_PREFIX", 24),
		NetworkIPv6:            goutils.EnvBool("MIABI_NETWORK_IPV6", false),
		NetworkIPv6ULAPrefix:   goutils.Env("MIABI_NETWORK_IPV6_ULA_PREFIX", ""),
		BuildTimeoutMinutes:    goutils.EnvInt("MIABI_BUILD_TIMEOUT_MINUTES", 30),
		RunnerWaitTimeout:      time.Duration(goutils.EnvInt("MIABI_RUNNER_WAIT_TIMEOUT_MINUTES", 30)) * time.Minute,
		JobAPITokenEnabled:     goutils.EnvBool("MIABI_JOB_API_TOKEN_ENABLED", true),
		securitySchemes:        okapi.SecuritySchemes{},
	}
}

// validate() refuses to boot in non-dev if any of these insecure defaults remain.
const (
	defaultJWTSecret     = "change-me-in-production"
	defaultAdminPassword = "admin@1234"
	minSecretLen         = 32
)

func (c *Config) validate() error {
	// Runs in dev too so a bad pool fails at boot rather than at the first network create.
	if err := c.validateNetworkPool(); err != nil {
		return err
	}
	// Always runs: the registry host decides image ownership and cannot change without a restart.
	if err := c.validateRegistry(); err != nil {
		return err
	}
	if c.BillingConnectionLocked && !c.BillingAPIEnabled {
		return errors.New("MIABI_BILLING_CONNECTION_LOCKED=true needs MIABI_BILLING_API_ENABLED=true")
	}
	if c.WorkspaceCreation != "" && !models.ValidWorkspaceCreation(c.WorkspaceCreation) {
		return fmt.Errorf(`MIABI_WORKSPACE_CREATION must be "anyone", "except_invited", "admins_only" or "console"`)
	}
	if c.WorkspaceCreation == models.WorkspaceCreationConsole && !c.BillingAPIEnabled {
		return errors.New("MIABI_WORKSPACE_CREATION=console needs MIABI_BILLING_API_ENABLED=true")
	}
	if c.Env == "dev" {
		return nil
	}
	// Non-dev: refuse to start with insecure defaults.
	if c.JWTSecret == "" || c.JWTSecret == defaultJWTSecret {
		return fmt.Errorf("MIABI_JWT_SECRET must be set to a non-default value in non-dev environments")
	}
	if len(c.JWTSecret) < minSecretLen {
		return fmt.Errorf("MIABI_JWT_SECRET must be at least %d characters in non-dev environments; generate one with `openssl rand -hex 32` (changing it signs everyone out)", minSecretLen)
	}
	if strings.TrimSpace(c.EncryptionKey) == "" {
		return fmt.Errorf("MIABI_ENCRYPTION_KEY must be set in non-dev environments; without it, secrets are stored unencrypted at rest")
	}
	if hasWildcardOrigin(c.CORSOrigins) {
		return fmt.Errorf(`MIABI_CORS_ORIGINS must be an explicit allowlist (not "*") in non-dev environments; a wildcard origin is unsafe with credentialed requests`)
	}
	return nil
}

// ValidateAdminSeed runs only when no admin exists, so later boots can drop the seed credentials.
func (c *Config) ValidateAdminSeed() error {
	if email := strings.TrimSpace(c.AdminEmail); email == "" || !strings.Contains(email, "@") {
		return fmt.Errorf("MIABI_ADMIN_EMAIL must be set to an email address to seed the platform admin")
	}
	if c.Env == "dev" {
		return nil
	}
	if c.AdminPassword == "" || c.AdminPassword == defaultAdminPassword {
		return fmt.Errorf("MIABI_ADMIN_PASSWORD must be set to a non-default value to seed the platform admin in non-dev environments")
	}
	if err := models.CheckNewPassword(c.AdminPassword); err != nil {
		return fmt.Errorf("MIABI_ADMIN_PASSWORD: %w", err)
	}
	return nil
}

func (c *Config) validateNetworkPool() error {
	// Only a hand-built Config (tests) is empty; managed networks then fall back to Docker.
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

// AllowedBrowserOrigins is the CORS allowlist plus the web UI URL; "*" (dev) means no restriction.
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

func hasWildcardOrigin(origins string) bool {
	for _, o := range strings.Split(origins, ",") {
		if strings.TrimSpace(o) == "*" {
			return true
		}
	}
	return false
}

// DeploymentURL binds a URL-scoped license; empty disables the license URL check.
func (c *Config) DeploymentURL() string {
	if strings.TrimSpace(c.AppWebURL) != "" {
		return c.AppWebURL
	}
	return c.ApiBaseURL
}

// Distinguishes "operator pinned this" from "admin UI owns it", which a zero value cannot.
func envPresent(key string) bool {
	_, ok := os.LookupEnv(key)
	return ok
}

// LogLevelFor errors on unknown values; "off" is refused because the library cannot honour it.
func (c *Config) LogLevelFor() (logger.LogLevel, error) {
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "":
		if c.DevMode {
			return logger.LevelDebug, nil
		}
		return logger.LevelInfo, nil
	case "debug":
		return logger.LevelDebug, nil
	case "info":
		return logger.LevelInfo, nil
	case "warn", "warning":
		return logger.LevelWarning, nil
	case "error":
		return logger.LevelError, nil
	case "off", "none", "silent":
		return "", fmt.Errorf("MIABI_LOG_LEVEL=%q is not supported: it would silence only part of "+
			"Miabi's logging and leave the rest running, which is more confusing than useful. "+
			"Use \"error\" for near-silence", c.LogLevel)
	}
	return "", fmt.Errorf("MIABI_LOG_LEVEL=%q is not a log level (use debug, info, warn or error)", c.LogLevel)
}

func (c *Config) DBLogLevel() gormlogger.LogLevel {
	level, err := c.LogLevelFor()
	if err != nil {
		return gormlogger.Error
	}
	switch level {
	case logger.LevelDebug:
		return gormlogger.Info
	case logger.LevelError:
		return gormlogger.Error
	default:
		return gormlogger.Warn
	}
}

// logger.New assigns the package-level logger, so every bare logger call honours this level.
func (c *Config) initLogger() (*logger.Logger, error) {
	level, err := c.LogLevelFor()
	if err != nil {
		return nil, err
	}
	opts := []logger.Option{logger.WithLevel(level)}
	// Human-readable text in dev, JSON in production (for log shippers), as before.
	if !c.DevMode {
		opts = append(opts, logger.WithJSONFormat())
	}
	return logger.New(opts...), nil
}

func (c *Config) Initialize(app *okapi.Okapi) error {
	if err := c.validate(); err != nil {
		return err
	}

	l, err := c.initLogger()
	if err != nil {
		return err
	}
	if c.DevMode {
		app.WithDebug()
	}
	app.WithPort(c.Port)
	app.WithLogger(l.Logger)
	_ = goutils.SetEnv("ENV", c.Env)

	// Only reachable in dev; make the downgrade to unencrypted-at-rest impossible to miss.
	if strings.TrimSpace(c.EncryptionKey) == "" {
		logger.Warn("MIABI_ENCRYPTION_KEY is not set — secrets are stored WITHOUT encryption at rest (permitted only in dev)")
	} else if len(c.EncryptionKey) < minSecretLen {
		// Warn, never refuse: replacing a live key would make every workspace key unrecoverable.
		logger.Warn("MIABI_ENCRYPTION_KEY is shorter than the recommended 32 characters; use `openssl rand -hex 32` for new installs")
	}

	// The client IP feeds rate limits, admin IP allowlists and the audit log.
	if len(c.TrustedProxies) > 0 {
		app.WithTrustedProxies(c.TrustedProxies...)
	}

	corsOrigins := strings.Split(c.CORSOrigins, ",")
	for i := range corsOrigins {
		corsOrigins[i] = strings.TrimSpace(corsOrigins[i])
	}
	// Browsers forbid "*" with credentials, so dev keeps the wildcard but drops credentials.
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
	app.Use(ignoreClientGone)
	return nil
}

// Every SSE stream ends with the client gone, and okapi would otherwise log each one as a failure.
func ignoreClientGone(c *okapi.Context) error {
	err := c.Next()
	if err != nil && errors.Is(err, context.Canceled) && c.Request().Context().Err() != nil {
		return nil
	}
	return err
}

// InitWorker honours MIABI_LOG_LEVEL like the server, since the worker configures its own logger.
func (c *Config) InitWorker() error {
	if _, err := c.initLogger(); err != nil {
		return err
	}
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

	db, err := storage.ConnectPostgres(dsn, c.DBLogLevel())
	if err != nil {
		logger.Fatal("failed to connect to database", "error", err)
	}
	c.Database.DB = db

	rdb, err := storage.NewRedis(c.Redis.Addr, c.Redis.Password, c.Redis.DB)
	if err != nil {
		logger.Fatal("failed to connect to redis", "error", err)
	}
	c.Redis.Client = rdb
}

// splitList parses a comma-separated setting, dropping blanks so "a, ,b," is [a b].
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// doNotTrack reads the cross-tool DO_NOT_TRACK convention: any value but empty, "0" or "false" opts out.
func doNotTrack(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no":
		return false
	}
	return true
}

// envRatio reads a multiplier of at least 1, so an unset, malformed or lower value keeps limits as they are.
func envRatio(name string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64)
	if err != nil || v < 1 || math.IsInf(v, 0) {
		return 1
	}
	return v
}
