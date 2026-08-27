// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package registryserver runs and authorizes the platform's built-in, multi-tenant Docker registry. The
// registry container runs auth-less on the gateway network; authentication is enforced at the edge by a
// Goma forwardAuth middleware calling Authorize. Distinct from services/registry (external credentials).
package registryserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

type imageResolver interface{ Ref(key string) string }

type settingsReader interface{ String(key, def string) string }

type keyVerifier interface {
	Verify(plaintext string) (*models.APIKey, error)
}

// entitlementChecker reports whether a licensed capability is usable (satisfied
// by enterprise.EE). A nil checker entitles nothing: the S3 driver is a paid
// feature, so an unwired checker must fail closed rather than grant it.
type entitlementChecker interface{ Has(flag string) bool }

type workspaceFinder interface {
	FindByID(id uint) (*models.Workspace, error)
	FindByName(name string) (*models.Workspace, error)
	FindMember(workspaceID, userID uint) (*models.WorkspaceMember, error)
}

// Service manages the registry settings, container lifecycle, and per-request
// authorization.
type Service struct {
	repo     *repositories.RegistrySettingsRepository
	images   imageResolver
	settings settingsReader
	keys     keyVerifier
	ws       workspaceFinder
	proxy    proxy.Manager
	reg      *Client
	usage    *usageCache
	network  string
	// internalNetwork is the platform's private network. The registry joins it so the CONTROL PLANE can
	// reach it — the control plane is not on the proxy network, and every browse, quota and GC call goes
	// to http://mb-registry:5000 directly. It keeps the proxy attachment for its own egress and for an
	// S3 backend that is really a self-hosted MinIO app. Empty on Compose.
	internalNetwork string
	controlURL      string
	cfg             config.RegistryConfig
	// ee gates the licensed S3 storage driver. Checked at every point the driver would actually be used
	// (container start, GC), not only where it is configured — the configuration now arrives from the
	// environment, which no API-level check can see.
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

// platformToken is the shared secret the platform's build/deploy worker uses to push and pull built images.
// It resolves lazily so it never depends on construction order relative to crypto.Init: an explicit
// MIABI_REGISTRY_PLATFORM_TOKEN wins, otherwise it derives deterministically from the master encryption key.
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

// SetInternalNetwork names the platform's private network, which is how the control plane reaches the
// registry once the stack is split. Unset on Compose, where the proxy network is the only fabric.
func (s *Service) SetInternalNetwork(name string) { s.internalNetwork = name }

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

// SaveInput carries a settings update. Every field is optional in the sense that
// an environment-pinned one is ignored — see Locks. Pointers mark "the client
// sent this"; nil leaves the stored value alone.
type SaveInput struct {
	DeleteEnabled       bool
	PerWorkspaceQuotaMB int

	// Platform configuration. Written only where the environment is silent.
	Enabled     *bool
	Host        *string
	StorageType *string

	S3Endpoint       *string
	S3Bucket         *string
	S3Region         *string
	S3AccessKey      *string
	S3ForcePathStyle *bool
	// S3SecretKey rotates the stored secret. Empty (or nil) keeps the current one,
	// so a form that never reads the value back can be saved without wiping it.
	S3SecretKey *string
}

// Locks reports which fields the environment has pinned. A locked field is read-only: Save ignores it, and
// the admin UI renders it as fixed with the variable that owns it, rather than offering an input whose
// value the server would discard.
type Locks struct {
	Enabled bool `json:"enabled"`
	Host    bool `json:"host"`
	Storage bool `json:"storage"`
	// S3 is per-field: an install may pin the bucket in the environment and leave
	// the credentials to the UI, or the reverse.
	S3Endpoint  bool `json:"s3_endpoint"`
	S3Bucket    bool `json:"s3_bucket"`
	S3Region    bool `json:"s3_region"`
	S3AccessKey bool `json:"s3_access_key"`
	S3SecretKey bool `json:"s3_secret_key"`
	S3ForcePath bool `json:"s3_force_path_style"`
}

// Any reports whether the environment pins anything at all.
func (l Locks) Any() bool {
	return l.Enabled || l.Host || l.Storage || l.S3Endpoint || l.S3Bucket ||
		l.S3Region || l.S3AccessKey || l.S3SecretKey || l.S3ForcePath
}

// Locks returns the environment-pinned fields for this install.
func (s *Service) Locks() Locks {
	c := s.cfg
	return Locks{
		Enabled:     c.EnabledSet,
		Host:        strings.TrimSpace(c.Host) != "",
		Storage:     strings.TrimSpace(c.StorageType) != "",
		S3Endpoint:  c.S3Endpoint != "",
		S3Bucket:    c.S3Bucket != "",
		S3Region:    c.S3Region != "",
		S3AccessKey: c.S3AccessKey != "",
		S3SecretKey: c.S3SecretKey != "",
		S3ForcePath: c.S3ForcePath,
	}
}

// Save persists the settings an admin may change, skipping every field the environment pins, so an install
// configured by compose or Helm keeps behaving as declared. Validating a *change* — S3 entitlement, host
// usability, whether switching storage strands blobs — belongs to the caller, where confirmation happens.
func (s *Service) Save(in SaveInput) (*models.RegistrySettings, error) {
	st, err := s.repo.Get()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = &models.RegistrySettings{StorageType: models.RegistryStorageFilesystem}
	} else if err != nil {
		return nil, err
	}
	locks := s.Locks()

	if in.Enabled != nil && !locks.Enabled {
		st.Enabled = *in.Enabled
	}
	if in.Host != nil && !locks.Host {
		host, hErr := NormalizeHost(*in.Host)
		if hErr != nil {
			return nil, fmt.Errorf("host: %w", hErr)
		}
		st.Host = host
	}
	if in.StorageType != nil && !locks.Storage {
		st.StorageType = normalizeStorage(*in.StorageType)
	}
	setStr := func(dst *string, src *string, locked bool) {
		if src != nil && !locked {
			*dst = strings.TrimSpace(*src)
		}
	}
	setStr(&st.S3Endpoint, in.S3Endpoint, locks.S3Endpoint)
	setStr(&st.S3Bucket, in.S3Bucket, locks.S3Bucket)
	setStr(&st.S3Region, in.S3Region, locks.S3Region)
	setStr(&st.S3AccessKey, in.S3AccessKey, locks.S3AccessKey)
	if in.S3ForcePathStyle != nil && !locks.S3ForcePath {
		st.S3ForcePathStyle = *in.S3ForcePathStyle
	}
	// A blank secret means "keep what is stored": the API never returns the value,
	// so a form round-trip carries nothing to re-send.
	if in.S3SecretKey != nil && !locks.S3SecretKey && strings.TrimSpace(*in.S3SecretKey) != "" {
		enc, eErr := crypto.Encrypt(strings.TrimSpace(*in.S3SecretKey))
		if eErr != nil {
			return nil, fmt.Errorf("encrypt S3 secret key: %w", eErr)
		}
		st.S3SecretKeyEnc = enc
	}

	// The data volume is a fixed platform name, not admin-configurable.
	st.VolumeName = models.DefaultRegistryVolume
	st.DeleteEnabled = in.DeleteEnabled
	st.PerWorkspaceQuotaMB = in.PerWorkspaceQuotaMB

	if err := s.repo.Upsert(st); err != nil {
		return nil, err
	}
	// Return the same env-layered view a read would give, so the caller renders
	// the effective configuration rather than the bare row.
	s.applyEnvConfig(st)
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	return st, nil
}

// normalizeStorage maps a driver name to a known one, defaulting to the filesystem driver. It is silent by
// design — it runs on every settings read. An unrecognized MIABI_REGISTRY_STORAGE is caught once, loudly,
// at boot (config.validateRegistry) rather than warned about on every call.
func normalizeStorage(t string) string {
	if strings.ToLower(strings.TrimSpace(t)) == models.RegistryStorageS3 {
		return models.RegistryStorageS3
	}
	return models.RegistryStorageFilesystem
}

// envHost is the validated MIABI_REGISTRY_HOST, or "" when unset or malformed. A malformed host is dropped
// rather than used: it is the string every image reference is matched against, and a value matching nothing
// would leave the tenant check silently inert while the registry still served traffic. Boot refuses it too.
func (s *Service) envHost() string {
	host, err := NormalizeHost(s.cfg.Host)
	if err != nil {
		logger.Error("registry: ignoring invalid MIABI_REGISTRY_HOST", "error", err)
		return ""
	}
	return host
}

// applyEnvConfig layers the boot environment over the stored settings. The environment wins wherever it speaks,
// and only there, so a compose- or Helm-declared install stays authoritative while one that sets none of these
// is configured from the UI. Locks reports which fields are pinned, so Save ignores them and the UI shows why.
func (s *Service) applyEnvConfig(st *models.RegistrySettings) {
	c := s.cfg
	// Only an explicitly-set variable pins enablement: absent means the stored
	// value (i.e. the admin UI's switch) decides.
	if c.EnabledSet {
		st.Enabled = c.Enabled
	}
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

// StorageSource names where the effective storage driver came from, so the admin UI can say why a field is
// fixed or which value it is editing: "env" (MIABI_REGISTRY_STORAGE pins it), "stored" (configured in the
// console), or "default" (the filesystem driver, nothing configured).
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
func (s *Service) StorageUnavailableReason(st *models.RegistrySettings) string {
	if st == nil || !st.UsesS3() {
		return ""
	}
	if !s.S3Entitled() {
		return "S3/MinIO storage for the built-in registry requires an Enterprise license (the registry_s3 entitlement); " +
			"install a license, or switch the storage driver back to a local volume"
	}
	if strings.TrimSpace(st.S3Bucket) == "" {
		return "S3 storage is selected but no bucket is configured"
	}
	return ""
}

// HostFor returns the effective registry hostname: MIABI_REGISTRY_HOST, else registry.<external-base-domain>,
// else empty. Both candidates are validated — an unusable host resolves to "", so distribution reports
// itself unavailable with a reason rather than silently failing to match any image reference.
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

// HostSource names where the effective host came from: "env" (pinned by
// MIABI_REGISTRY_HOST), "stored" (set in the console), "base_domain" (derived
// from the platform's external base domain), or "unset".
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
	// Refuse to bring up storage this install may not use, tearing down first: an install whose license lapsed may
	// already have a registry serving from that bucket, and leaving it running would make the check advisory.
	// Data in the bucket is untouched — only the container serving it stops.
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

// warnIfDisabledByLock reports a registry switched on in the console but held off by the environment. Honouring
// the environment is correct, but doing so silently is not: the console shows a registry that never comes up and
// git-source deploys fail with no clue. Only fires when the variable is explicitly false, not when absent.
func (s *Service) warnIfDisabledByLock() {
	if !s.cfg.EnabledSet || s.cfg.Enabled || s.repo == nil {
		return
	}
	stored, err := s.repo.Get()
	if err != nil || stored == nil || !stored.Enabled {
		return
	}
	logger.Error("internal registry is enabled in the console but MIABI_REGISTRY_ENABLED=false pins it off — " +
		"the environment wins where it is set. Remove the variable to let the console own the switch, or set it to true.")
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
	nets, err := s.networks(ctx, dc)
	if err != nil {
		return err
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
		Networks:       nets,
		NetworkAliases: []string{Alias},
		Mounts:         mounts,
		RestartPolicy:  "unless-stopped",
		Labels:         docker.PlatformLabels(docker.RoleRegistry, docker.ManagedByMiabi, nil),
	}); err != nil {
		return fmt.Errorf("run registry container: %w", err)
	}
	return nil
}

// networks are the fabrics the registry container joins: the proxy network, for its own egress and for
// an S3 backend that is really a self-hosted MinIO app, and the platform's private network, where the
// control plane and the gateway can reach it. The alias is registered on both, so
// http://mb-registry:5000 resolves from either side.
//
// The proxy network is first because Docker picks the container's default route from its attachments,
// and a registry on the S3 driver has to get out to the bucket.
func (s *Service) networks(ctx context.Context, dc docker.Client) ([]string, error) {
	var out []string
	for _, name := range []string{s.network, s.internalNetwork} {
		if name == "" || slices.Contains(out, name) {
			continue
		}
		if _, err := dc.EnsureNetwork(ctx, name); err != nil {
			return nil, fmt.Errorf("ensure network %q: %w", name, err)
		}
		out = append(out, name)
	}
	return out, nil
}

// GarbageCollect reclaims storage from deleted or overwritten manifests. To run safely it flips the
// registry read-only (pulls keep working, pushes pause), runs `registry garbage-collect` as a one-shot
// against the same storage, then restores read-write. A no-op unless the registry is enabled with deletes on.
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

func (s *Service) proxyConfig(st *models.RegistrySettings, enabled bool) proxy.RegistryProxy {
	return proxy.RegistryProxy{
		Enabled:     enabled,
		Host:        s.HostFor(st),
		Upstream:    fmt.Sprintf("http://%s:%d", Alias, Port),
		AuthURL:     s.authURL(),
		TLSProvider: s.settings.String(settings.KeyExternalBaseProvider, ""),
		// Off only for an install behind a TLS terminator with no trusted proxies
		// configured on the gateway, where the redirect would loop.
		HTTPSRedirect: s.cfg.HTTPSRedirect,
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
