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

// Get returns the current settings (an empty, disabled default when unset),
// never exposing the S3 secret — only the S3SecretSet presence flag.
func (s *Service) Get() (*models.RegistrySettings, error) {
	st, err := s.repo.Get()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = &models.RegistrySettings{StorageType: models.RegistryStorageFilesystem, VolumeName: models.DefaultRegistryVolume}
	} else if err != nil {
		return nil, err
	}
	s.applyEnvOverrides(st)
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	return st, nil
}

// SaveInput carries an update. S3SecretKey is nil/empty to keep the stored secret.
type SaveInput struct {
	Enabled             bool
	Host                string
	StorageType         string
	S3Endpoint          string
	S3Bucket            string
	S3Region            string
	S3AccessKey         string
	S3SecretKey         *string
	S3ForcePathStyle    bool
	DeleteEnabled       bool
	PerWorkspaceQuotaMB int
}

// Save persists the settings, encrypting a newly supplied S3 secret and
// preserving the existing one otherwise. The secret is never returned.
func (s *Service) Save(in SaveInput) (*models.RegistrySettings, error) {
	st, err := s.repo.Get()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = &models.RegistrySettings{}
	} else if err != nil {
		return nil, err
	}

	st.Enabled = in.Enabled
	st.Host = strings.TrimSpace(in.Host)
	st.StorageType = normalizeStorage(in.StorageType)
	// The data volume is a fixed platform name, not admin-configurable.
	st.VolumeName = models.DefaultRegistryVolume
	st.S3Endpoint = strings.TrimSpace(in.S3Endpoint)
	st.S3Bucket = strings.TrimSpace(in.S3Bucket)
	st.S3Region = strings.TrimSpace(in.S3Region)
	st.S3AccessKey = strings.TrimSpace(in.S3AccessKey)
	st.S3ForcePathStyle = in.S3ForcePathStyle
	st.DeleteEnabled = in.DeleteEnabled
	st.PerWorkspaceQuotaMB = in.PerWorkspaceQuotaMB

	// Only replace the secret when a non-empty new value is supplied.
	if in.S3SecretKey != nil && *in.S3SecretKey != "" {
		enc, err := crypto.Encrypt(*in.S3SecretKey)
		if err != nil {
			return nil, err
		}
		st.S3SecretKeyEnc = enc
	}

	if err := s.repo.Upsert(st); err != nil {
		return nil, err
	}
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	return st, nil
}

func normalizeStorage(t string) string {
	if strings.TrimSpace(t) == models.RegistryStorageS3 {
		return models.RegistryStorageS3
	}
	return models.RegistryStorageFilesystem
}

// applyEnvOverrides layers boot-authoritative env config over stored settings,
// mirroring the ExternalBaseDomain convention: a set env field wins on boot.
func (s *Service) applyEnvOverrides(st *models.RegistrySettings) {
	c := s.cfg
	if !c.IsSet() {
		return
	}
	if c.Enabled {
		st.Enabled = true
	}
	if c.Host != "" {
		st.Host = c.Host
	}
	if c.StorageType != "" {
		st.StorageType = normalizeStorage(c.StorageType)
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
		}
	}
	if c.S3ForcePath {
		st.S3ForcePathStyle = true
	}
}

// HostFor returns the effective registry hostname: the configured host, else
// registry.<external-base-domain>, else empty (registry can't be served).
func (s *Service) HostFor(st *models.RegistrySettings) string {
	if st.Host != "" {
		return st.Host
	}
	if base := strings.TrimSpace(s.settings.String(settings.KeyExternalBaseDomain, "")); base != "" {
		return "registry." + base
	}
	return ""
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
		return s.Teardown(ctx, dc)
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
