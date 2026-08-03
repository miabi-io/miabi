// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package registryserver runs and authorizes the platform's built-in,
// multi-tenant Docker registry (CNCF distribution / registry:3). The registry
// container runs auth-less on the gateway network; authentication is enforced at
// the edge by a Goma forwardAuth middleware that calls Authorize (see auth.go).
// Distinct from internal/services/registry, which manages external (third-party)
// registry credentials.
package registryserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// ContainerName / Alias are the registry container's name and its gateway-network
// DNS alias (the route upstream is http://mb-registry:5000).
const (
	ContainerName = "mb-registry"
	Alias         = "mb-registry"
	Port          = 5000
	dataPath      = "/var/lib/registry"
)

// imageResolver resolves the registry image from the platform-image catalog.
type imageResolver interface{ Ref(key string) string }

// settingsReader reads platform string settings (the external base domain).
type settingsReader interface{ String(key, def string) string }

// keyVerifier verifies an API token (satisfied by *auth.APIKeyService).
type keyVerifier interface {
	Verify(plaintext string) (*models.APIKey, error)
}

// entitlementChecker reports whether a licensed capability is usable (satisfied
// by enterprise.EE). A nil checker entitles nothing: the S3 driver is a paid
// feature, so an unwired checker must fail closed rather than grant it.
type entitlementChecker interface{ Has(flag string) bool }

// workspaceFinder resolves workspaces and memberships for authorization
// (satisfied by the workspace repo).
type workspaceFinder interface {
	FindByID(id uint) (*models.Workspace, error)
	FindByName(name string) (*models.Workspace, error)
	FindMember(workspaceID, userID uint) (*models.WorkspaceMember, error)
}

// Service manages the registry settings, container lifecycle, and per-request
// authorization.
type Service struct {
	repo       *repositories.RegistrySettingsRepository
	images     imageResolver
	settings   settingsReader
	keys       keyVerifier
	ws         workspaceFinder
	proxy      proxy.Manager
	reg        *Client
	usage      *usageCache
	network    string
	controlURL string
	cfg        config.RegistryConfig
	// ee gates the licensed S3 storage driver. Checked at every point the driver
	// would actually be used (container start, GC), not only where it is
	// configured — the configuration now arrives from the environment, which no
	// API-level check can see.
	ee entitlementChecker
	// catalog is what the platform knows about the images it built: the digests a
	// live deployment or pinned release holds (so a tag delete can't pull an image
	// out from under a running release), and the build behind a digest. nil-safe.
	catalog Catalog
}

// platformTokenLabel keys the derived registry platform credential. Changing it
// rotates the token (never do so casually — in-flight pulls would need the new
// value), so it is a stable constant.
const platformTokenLabel = "registry:platform-token"

// platformToken is the shared secret the platform's own build/deploy worker uses
// to push and pull built images (the registry recognizes it as the platform
// principal). It is resolved lazily so it never depends on service-construction
// order relative to crypto.Init: an explicit MIABI_REGISTRY_PLATFORM_TOKEN wins
// (for operators who want to share it with external tooling), otherwise it is
// derived deterministically from the master encryption key — so the platform
// manages it internally with no operator action and every process agrees.
func (s *Service) platformToken() string {
	if t := strings.TrimSpace(s.cfg.PlatformToken); t != "" {
		return t
	}
	return crypto.DeriveToken(platformTokenLabel)
}

// NewService wires the registry service. network is the gateway Docker network;
// controlURL is the address the gateway reaches Miabi at (forwardAuth fallback).
func NewService(
	repo *repositories.RegistrySettingsRepository,
	images imageResolver,
	settingsReader settingsReader,
	keys keyVerifier,
	ws workspaceFinder,
	proxyMgr proxy.Manager,
	network string,
	controlURL string,
	cfg config.RegistryConfig,
) *Service {
	return &Service{
		repo: repo, images: images, settings: settingsReader, keys: keys, ws: ws,
		proxy: proxyMgr, reg: NewClient(fmt.Sprintf("http://%s:%d", Alias, Port)),
		usage:   newUsageCache(),
		network: network, controlURL: controlURL, cfg: cfg,
	}
}

func (s *Service) SetEntitlements(ee entitlementChecker) { s.ee = ee }

// S3Entitled reports whether this install may use the S3 storage driver.
func (s *Service) S3Entitled() bool {
	return s.ee != nil && s.ee.Has(enterprise.FlagRegistryS3)
}

// Get returns the current settings (an empty, disabled default when unset),
// never exposing the S3 secret — only the S3SecretSet presence flag.
func (s *Service) Get() (*models.RegistrySettings, error) {
	var st *models.RegistrySettings
	var err error
	if s.repo != nil {
		st, err = s.repo.Get()
	} else {
		// No settings store wired. Enablement and the host come from the
		// environment regardless, so the service can still answer the questions the
		// deploy path asks of it (is this ref ours, whose namespace is it).
		err = gorm.ErrRecordNotFound
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = &models.RegistrySettings{StorageType: models.RegistryStorageFilesystem, VolumeName: models.DefaultRegistryVolume}
	} else if err != nil {
		return nil, err
	}
	s.applyEnvConfig(st)
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	return st, nil
}

// SaveInput carries an update: the two operational settings an admin may change
// at runtime.
//
// Enablement, the hostname, and the whole storage configuration are deliberately
// absent — all of them are boot-time, environment-only (see applyEnvConfig).
// Accepting any of them here would give the admin API a way to move the
// registry's identity or its storage backend at runtime, and would let the S3
// driver be selected without the entitlement the environment path is checked
// against.
type SaveInput struct {
	DeleteEnabled       bool
	PerWorkspaceQuotaMB int
}

// Save persists the two mutable settings. Storage — driver, bucket, credentials
// — is never written here: it is read from the environment on every load, so a
// stored value could only ever go stale against it.
func (s *Service) Save(in SaveInput) (*models.RegistrySettings, error) {
	st, err := s.repo.Get()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = &models.RegistrySettings{StorageType: models.RegistryStorageFilesystem}
	} else if err != nil {
		return nil, err
	}

	st.Enabled = s.cfg.Enabled
	if host := s.envHost(); host != "" {
		st.Host = host
	}
	// The data volume is a fixed platform name, not admin-configurable.
	st.VolumeName = models.DefaultRegistryVolume
	st.DeleteEnabled = in.DeleteEnabled
	st.PerWorkspaceQuotaMB = in.PerWorkspaceQuotaMB

	if err := s.repo.Upsert(st); err != nil {
		return nil, err
	}
	// Return the same env-layered view a read would give, so the caller renders
	// the effective storage rather than the bare row.
	s.applyEnvConfig(st)
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	return st, nil
}

// normalizeStorage maps a driver name to a known one, defaulting to the
// filesystem driver. It is silent by design — it runs on every settings read.
// An unrecognized MIABI_REGISTRY_STORAGE is caught once, loudly, at boot
// (config.validateRegistry) rather than warned about on every call.
func normalizeStorage(t string) string {
	if strings.ToLower(strings.TrimSpace(t)) == models.RegistryStorageS3 {
		return models.RegistryStorageS3
	}
	return models.RegistryStorageFilesystem
}

// envHost is the validated MIABI_REGISTRY_HOST, or "" when unset or malformed.
// A malformed host is dropped rather than used: it is the string every image
// reference is matched against, and a value that matches nothing would leave the
// tenant check silently inert while the registry still served traffic. Boot
// refuses such a value outright (config.validate), so reaching here means the
// process was started before the check existed.
func (s *Service) envHost() string {
	host, err := NormalizeHost(s.cfg.Host)
	if err != nil {
		logger.Error("registry: ignoring invalid MIABI_REGISTRY_HOST", "error", err)
		return ""
	}
	return host
}

// applyEnvConfig layers the boot environment over the stored settings.
//
// Enablement, the hostname, and the storage configuration are all
// environment-derived: nothing in the admin API writes them (see SaveInput).
// They define whether the registry exists, what name every image reference is
// anchored to, and where the bytes live — none of which may change under a
// running platform. The gateway route, its TLS certificate, the references
// recorded on past deployments, and the namespace check at pull time all key off
// the host; every pushed blob keys off the storage backend. Changing any of them
// takes an env change and a restart, so every process in the install agrees on
// the answer.
//
// A stored value survives only where the environment is silent, and only as a
// legacy carry-over from when these fields were UI-editable — an upgraded
// install must keep answering on the host and reading from the bucket its
// existing images already live behind. Nothing can write a new one.
func (s *Service) applyEnvConfig(st *models.RegistrySettings) {
	c := s.cfg
	st.Enabled = c.Enabled
	if host := s.envHost(); host != "" {
		st.Host = host
	}
	if c.StorageType != "" {
		st.StorageType = normalizeStorage(c.StorageType)
	} else {
		// Normalize the stored value too: it may predate the driver names, and an
		// unrecognized one must not read as "not filesystem" anywhere downstream.
		st.StorageType = normalizeStorage(st.StorageType)
	}
	if c.S3Endpoint != "" {
		st.S3Endpoint = c.S3Endpoint
	}
	if c.S3Bucket != "" {
		st.S3Bucket = c.S3Bucket
	}
	if c.S3Region != "" {
		st.S3Region = c.S3Region
	}
	if c.S3AccessKey != "" {
		st.S3AccessKey = c.S3AccessKey
	}
	if c.S3SecretKey != "" {
		if enc, err := crypto.Encrypt(c.S3SecretKey); err == nil {
			st.S3SecretKeyEnc = enc
		} else {
			logger.Error("registry: failed to encrypt MIABI_REGISTRY_S3_SECRET_KEY", "error", err)
		}
	}
	if c.S3ForcePath {
		st.S3ForcePathStyle = true
	}
}

// StorageSource names where the effective storage driver came from, for the
// admin UI's read-only explanation of why the fields cannot be edited:
// "env" (MIABI_REGISTRY_STORAGE), "stored" (a value saved while the driver was
// UI-editable), or "default" (the filesystem driver, nothing configured).
func (s *Service) StorageSource(st *models.RegistrySettings) string {
	if strings.TrimSpace(s.cfg.StorageType) != "" {
		return "env"
	}
	if st != nil && st.UsesS3() {
		return "stored"
	}
	return "default"
}

// StorageUnavailableReason returns "" when the configured storage driver can be
// used, else a sentence naming exactly what is wrong.
//
// This is where the S3 entitlement is actually enforced. The admin API cannot
// select the driver any more, so a check there would guard a door nobody uses:
// the configuration arrives from the environment, and the environment is only
// read here, on the paths that start a container against that storage.
func (s *Service) StorageUnavailableReason(st *models.RegistrySettings) string {
	if st == nil || !st.UsesS3() {
		return ""
	}
	if !s.S3Entitled() {
		return "S3/MinIO storage for the built-in registry requires an Enterprise license (the registry_s3 entitlement); " +
			"install a license, or set MIABI_REGISTRY_STORAGE=filesystem to use a local volume"
	}
	if strings.TrimSpace(st.S3Bucket) == "" {
		return "S3 storage is selected but no bucket is configured (set MIABI_REGISTRY_S3_BUCKET)"
	}
	return ""
}

// HostFor returns the effective registry hostname: MIABI_REGISTRY_HOST, else
// registry.<external-base-domain>, else empty (registry can't be served).
//
// Both candidates are validated. An unusable host resolves to "" — distribution
// then reports itself unavailable with a specific reason and no gateway route is
// written — rather than to a string that would silently fail to match any image
// reference.
func (s *Service) HostFor(st *models.RegistrySettings) string {
	if host := s.envHost(); host != "" {
		return host
	}
	// A legacy value stored back when the host was UI-editable. Still honored so an
	// upgraded install keeps serving on the name its images already reference, but
	// it is no longer writable — the admin API and UI treat the host as read-only.
	if host, err := NormalizeHost(st.Host); err == nil && host != "" {
		return host
	}
	if s.settings == nil {
		return ""
	}
	base := strings.TrimSpace(s.settings.String(settings.KeyExternalBaseDomain, ""))
	if base == "" {
		return ""
	}
	host, err := NormalizeHost("registry." + base)
	if err != nil {
		logger.Error("registry: external base domain does not yield a usable registry host", "base_domain", base, "error", err)
		return ""
	}
	return host
}

// HostSource names where the effective host came from, for the admin UI's
// read-only explanation of why the field cannot be edited.
func (s *Service) HostSource(st *models.RegistrySettings) string {
	if s.envHost() != "" {
		return "env"
	}
	if stored, err := NormalizeHost(st.Host); err == nil && stored != "" {
		return "stored"
	}
	if s.HostFor(st) != "" {
		return "base_domain"
	}
	return "unset"
}

// image resolves the registry image (env override → catalog → registry:3).
func (s *Service) image() string {
	if s.cfg.Image != "" {
		return s.cfg.Image
	}
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyRegistry); r != "" {
			return r
		}
	}
	return "registry:3"
}

// renderEnv builds the registry container env from the storage settings. When
// readonly is set, storage maintenance read-only mode is enabled so the registry
// keeps serving pulls while a garbage-collect runs against the same storage.
func (s *Service) renderEnv(st *models.RegistrySettings, readonly bool) ([]string, error) {
	env := []string{fmt.Sprintf("REGISTRY_HTTP_ADDR=:%d", Port)}
	if st.DeleteEnabled {
		env = append(env, "REGISTRY_STORAGE_DELETE_ENABLED=true")
	}
	if readonly {
		env = append(env, "REGISTRY_STORAGE_MAINTENANCE_READONLY_ENABLED=true")
	}
	if st.UsesS3() {
		// Defense in depth: no caller may render an S3 config this install is not
		// licensed for, whichever path got here.
		if reason := s.StorageUnavailableReason(st); reason != "" {
			return nil, errors.New(reason)
		}
		secret := ""
		if st.S3SecretKeyEnc != "" {
			dec, err := crypto.Decrypt(st.S3SecretKeyEnc)
			if err != nil {
				return nil, fmt.Errorf("decrypt registry s3 secret: %w", err)
			}
			secret = dec
		}
		env = append(env,
			"REGISTRY_STORAGE=s3",
			"REGISTRY_STORAGE_S3_BUCKET="+st.S3Bucket,
			"REGISTRY_STORAGE_S3_REGION="+st.S3Region,
			"REGISTRY_STORAGE_S3_ACCESSKEY="+st.S3AccessKey,
			"REGISTRY_STORAGE_S3_SECRETKEY="+secret,
		)
		if st.S3Endpoint != "" {
			env = append(env, "REGISTRY_STORAGE_S3_REGIONENDPOINT="+st.S3Endpoint)
		}
		if st.S3ForcePathStyle {
			env = append(env, "REGISTRY_STORAGE_S3_FORCEPATHSTYLE=true")
		}
		return env, nil
	}
	// filesystem driver (default).
	env = append(env,
		"REGISTRY_STORAGE=filesystem",
		"REGISTRY_STORAGE_FILESYSTEM_ROOTDIRECTORY="+dataPath,
	)
	return env, nil
}

// Ensure (re)creates the registry container per the current settings on the
// gateway network and seeds its gateway route. Idempotent. A no-op (teardown)
// when disabled. dc is the control-plane Docker client.
func (s *Service) Ensure(ctx context.Context, dc docker.Client) error {
	st, err := s.Get()
	if err != nil {
		return err
	}
	if !st.Enabled {
		s.warnIfDisabledByLock()
		return s.Teardown(ctx, dc)
	}
	// Refuse to bring up storage this install may not use. Tear down first: an
	// install whose license lapsed (or that was pointed at S3 by hand) may have a
	// registry already serving from that bucket, and leaving it running would make
	// the check advisory. Data in the bucket is untouched — only the container
	// serving it stops.
	if reason := s.StorageUnavailableReason(st); reason != "" {
		logger.Error("internal registry not started: " + reason)
		if err := s.Teardown(ctx, dc); err != nil {
			logger.Warn("registry: teardown after storage check failed", "error", err)
		}
		return errors.New(reason)
	}
	if s.HostFor(st) == "" {
		// The container can run, but nothing can reach it: the gateway route below
		// is what terminates TLS and enforces forwardAuth, and it needs a hostname.
		// Say so plainly — otherwise the only symptom is pulls failing on nodes.
		logger.Error("internal registry is enabled but has no usable hostname — set MIABI_REGISTRY_HOST or an external base domain and restart; no gateway route will be published")
	}
	if err := s.startContainer(ctx, dc, st, false); err != nil {
		return err
	}
	// Seed the gateway route + middlewares (HTTPS redirect, forwardAuth, namespace
	// rewrite). Best-effort: the container is up regardless of a gateway hiccup.
	if s.proxy != nil {
		if err := s.proxy.SyncRegistry(ctx, s.proxyConfig(st, true)); err != nil {
			logger.Warn("registry: seed gateway route failed", "error", err)
		}
	}
	logger.Info("internal registry ready", "image", s.image(), "storage", st.StorageType, "host", s.HostFor(st))
	return nil
}

// warnIfDisabledByLock reports an install that had the registry switched on from
// the admin UI before enablement became environment-only. Tearing it down on the
// first boot after the upgrade is correct — the environment is now the only
// answer — but silently is not: git-source deploys stop working, and nothing on
// the settings page would explain why. Say exactly what to set.
func (s *Service) warnIfDisabledByLock() {
	if s.cfg.Enabled || s.repo == nil {
		return
	}
	stored, err := s.repo.Get()
	if err != nil || stored == nil || !stored.Enabled {
		return
	}
	logger.Error("internal registry was enabled in the database but MIABI_REGISTRY_ENABLED is not set — " +
		"enablement and the registry hostname are now environment-only and take a restart to change. " +
		"The registry has been torn down; set MIABI_REGISTRY_ENABLED=true (and MIABI_REGISTRY_HOST, if you had a custom host) and restart Miabi to bring it back.")
}

// volumeMounts is the registry's data-volume mount (filesystem driver only). The
// volume name is the fixed platform default — not admin-configurable, so the data
// location is stable and predictable.
func (s *Service) volumeMounts(st *models.RegistrySettings) map[string]string {
	if st.UsesS3() {
		return map[string]string{}
	}
	return map[string]string{models.DefaultRegistryVolume: dataPath}
}

// startContainer pulls the image and (re)creates the registry container with the
// rendered storage env, read-write or read-only. Idempotent.
func (s *Service) startContainer(ctx context.Context, dc docker.Client, st *models.RegistrySettings, readonly bool) error {
	if _, err := dc.EnsureNetwork(ctx, s.network); err != nil {
		return fmt.Errorf("ensure network %q: %w", s.network, err)
	}
	mounts := s.volumeMounts(st)
	if !st.UsesS3() {
		for vol := range mounts {
			if _, err := dc.CreateVolume(ctx, vol, docker.PlatformLabels(docker.RoleRegistry, docker.ManagedByMiabi, nil), 0); err != nil {
				return fmt.Errorf("ensure registry volume %q: %w", vol, err)
			}
		}
	}
	env, err := s.renderEnv(st, readonly)
	if err != nil {
		return err
	}
	img := s.image()
	if err := dc.PullImage(ctx, img, nil); err != nil {
		return fmt.Errorf("pull registry image %q: %w", img, err)
	}
	_ = dc.RemoveContainer(ctx, ContainerName, true)
	if _, err := dc.RunContainer(ctx, docker.RunSpec{
		Name:           ContainerName,
		Image:          img,
		Env:            env,
		Networks:       []string{s.network},
		NetworkAliases: []string{Alias},
		Mounts:         mounts,
		RestartPolicy:  "unless-stopped",
		Labels:         docker.PlatformLabels(docker.RoleRegistry, docker.ManagedByMiabi, nil),
	}); err != nil {
		return fmt.Errorf("run registry container: %w", err)
	}
	return nil
}

// GarbageCollect reclaims storage from deleted/overwritten manifests. To run
// safely it flips the registry into read-only mode (pulls keep working, pushes
// pause), runs `registry garbage-collect` as a one-shot against the same
// storage, then restores read-write. A no-op unless the registry is enabled with
// deletes on. dc is the control-plane Docker client.
func (s *Service) GarbageCollect(ctx context.Context, dc docker.Client) error {
	st, err := s.Get()
	if err != nil {
		return err
	}
	if !st.Enabled || !st.DeleteEnabled {
		return nil
	}
	if reason := s.StorageUnavailableReason(st); reason != "" {
		return errors.New("registry gc: " + reason)
	}

	// 1. Read-only so no writes race the collector.
	if err := s.startContainer(ctx, dc, st, true); err != nil {
		return fmt.Errorf("registry gc: enter read-only: %w", err)
	}
	// 3. Always restore read-write, even if GC fails.
	defer func() {
		if err := s.startContainer(ctx, dc, st, false); err != nil {
			logger.Error("registry gc: failed to restore read-write — registry left read-only", "error", err)
		}
	}()

	// 2. One-shot collector over the same storage. Override the image entrypoint
	// (which would otherwise `serve`) to run the garbage-collect subcommand.
	env, err := s.renderEnv(st, false)
	if err != nil {
		return err
	}
	code, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:       ContainerName + "-gc",
		Image:      s.image(),
		Entrypoint: []string{"registry"},
		Cmd:        []string{"garbage-collect", "/etc/docker/registry/config.yml"},
		Env:        env,
		Mounts:     s.volumeMounts(st),
		Labels:     map[string]string{docker.LabelRole: docker.RoleRegistryGC}, // transient: deliberately not protected
	})
	if err != nil {
		return fmt.Errorf("registry gc: run collector: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("registry gc: collector exited %d: %s", code, out)
	}
	logger.Info("registry garbage-collect complete")
	return nil
}

// Teardown removes the registry container and its gateway route (best-effort).
// Data volumes are kept.
func (s *Service) Teardown(ctx context.Context, dc docker.Client) error {
	if s.proxy != nil {
		if err := s.proxy.SyncRegistry(ctx, proxy.RegistryProxy{Enabled: false}); err != nil {
			logger.Warn("registry teardown: remove gateway route", "error", err)
		}
	}
	if err := dc.RemoveContainer(ctx, ContainerName, true); err != nil {
		logger.Warn("registry teardown: remove container", "error", err)
	}
	return nil
}

// authURL is the forwardAuth target: the configured override, else the control
// URL, with the auth path appended.
func (s *Service) authURL() string {
	base := strings.TrimRight(firstNonEmpty(s.cfg.AuthURL, s.controlURL), "/")
	if base == "" {
		return ""
	}
	return base + "/internal/registry/auth"
}

// proxyConfig builds the gateway config for the current settings.
func (s *Service) proxyConfig(st *models.RegistrySettings, enabled bool) proxy.RegistryProxy {
	return proxy.RegistryProxy{
		Enabled:     enabled,
		Host:        s.HostFor(st),
		Upstream:    fmt.Sprintf("http://%s:%d", Alias, Port),
		AuthURL:     s.authURL(),
		TLSProvider: s.settings.String(settings.KeyExternalBaseProvider, ""),
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
