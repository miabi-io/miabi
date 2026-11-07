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

// Package registryserver runs the built-in multi-tenant registry; tenant requests are authorized via Goma forwardAuth.
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
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// The route upstream is http://mb-registry:5000 on the private network.
const (
	ContainerName = "mb-registry"
	Alias         = "mb-registry"
	Port          = 5000
	dataPath      = "/var/lib/registry"
)

type imageResolver interface{ Ref(key string) string }

// The registry is published under the default cluster's external domain.
type externalAccessReader interface {
	ExternalAccess(clusterID uint) (domain, provider string)
}

type keyVerifier interface {
	Verify(plaintext string) (*models.APIKey, error)
}

// nil means an app-bound key is refused rather than allowed everywhere.
type appNamer interface {
	FindByID(id uint) (*models.Application, error)
}

// The S3 driver is paid, so a nil checker must fail closed.
type entitlementChecker interface{ Has(flag string) bool }

type workspaceFinder interface {
	FindByID(id uint) (*models.Workspace, error)
	FindByName(name string) (*models.Workspace, error)
	FindMember(workspaceID, userID uint) (*models.WorkspaceMember, error)
}

type Service struct {
	repo     *repositories.RegistrySettingsRepository
	images   imageResolver
	external externalAccessReader
	keys     keyVerifier
	ws       workspaceFinder
	apps     appNamer
	proxy    proxy.Manager
	reg      *Client
	usage    *usageCache
	// Fallback only, for a Compose stack with no private network.
	network string
	// No tenant container can dial the registry here; empty on stacks predating the split.
	internalNetwork string
	controlURL      string
	cfg             config.RegistryConfig
	// Checked wherever the driver is used, since env-supplied config bypasses API-level checks.
	ee entitlementChecker
	// Lets tag deletes avoid pulling an image from under a running release; nil-safe.
	catalog Catalog
	// classes places the filesystem driver's volume on a storage class. nil keeps it in Docker's data root.
	classes StorageClasses
}

// StorageClasses resolves and prepares the storage class the registry's data volume lands on.
type StorageClasses interface {
	Resolve(serverID uint, name string) (*models.StorageClass, error)
	EnsureDir(ctx context.Context, c *models.StorageClass, dockerName string) error
	LocalID() uint
}

// Changing it rotates the token, breaking in-flight pulls, so it is a stable constant.
const platformTokenLabel = "registry:platform-token"

// Resolves lazily to avoid depending on crypto.Init order; MIABI_REGISTRY_PLATFORM_TOKEN wins over derivation.
func (s *Service) platformToken() string {
	if t := strings.TrimSpace(s.cfg.PlatformToken); t != "" {
		return t
	}
	return crypto.DeriveToken(platformTokenLabel)
}

// controlURL is where the gateway reaches Miabi (forwardAuth fallback).
func NewService(
	repo *repositories.RegistrySettingsRepository,
	images imageResolver,
	external externalAccessReader,
	keys keyVerifier,
	ws workspaceFinder,
	proxyMgr proxy.Manager,
	network string,
	controlURL string,
	cfg config.RegistryConfig,
) *Service {
	s := &Service{
		repo: repo, images: images, external: external, keys: keys, ws: ws,
		proxy:   proxyMgr,
		usage:   newUsageCache(),
		network: network, controlURL: controlURL, cfg: cfg,
	}
	// Built after the struct so the client can close over s: crypto.Init may not have set the key yet.
	s.reg = NewClient(fmt.Sprintf("http://%s:%d", Alias, Port), s.upstreamAuthHeader)
	return s
}

// SetInternalNetwork names the only network the registry joins.
func (s *Service) SetInternalNetwork(name string) { s.internalNetwork = name }

func (s *Service) SetEntitlements(ee entitlementChecker) { s.ee = ee }

// SetStorageClasses wires storage-class placement for the filesystem driver's data volume.
func (s *Service) SetStorageClasses(c StorageClasses) { s.classes = c }

// SetApps holds app-bound job credentials to their repository; without it such keys are refused on push.
func (s *Service) SetApps(a appNamer) { s.apps = a }

// S3Entitled reports whether this install may use the S3 storage driver.
func (s *Service) S3Entitled() bool {
	return s.ee != nil && s.ee.Has(enterprise.FlagRegistryS3)
}

// Get never exposes the S3 secret, only the S3SecretSet presence flag.
func (s *Service) Get() (*models.RegistrySettings, error) {
	var st *models.RegistrySettings
	var err error
	if s.repo != nil {
		st, err = s.repo.Get()
	} else {
		// Env still provides enablement and host, so the deploy path's questions remain answerable.
		err = gorm.ErrRecordNotFound
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = &models.RegistrySettings{StorageType: models.RegistryStorageFilesystem}
	} else if err != nil {
		return nil, err
	}
	s.applyEnvConfig(st)
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	return st, nil
}

// Env-pinned fields are ignored (see Locks); nil pointers leave the stored value alone.
type SaveInput struct {
	DeleteEnabled       bool
	PerWorkspaceQuotaMB int

	// Platform configuration. Written only where the environment is silent.
	Enabled      *bool
	Host         *string
	StorageType  *string
	StorageClass *string

	S3Endpoint       *string
	S3Bucket         *string
	S3Region         *string
	S3AccessKey      *string
	S3ForcePathStyle *bool
	// Empty keeps the current secret, so a form that never reads it back doesn't wipe it.
	S3SecretKey *string
}

// Locked fields are read-only: Save ignores them and the UI shows the owning variable.
type Locks struct {
	Enabled      bool `json:"enabled"`
	Host         bool `json:"host"`
	Storage      bool `json:"storage"`
	StorageClass bool `json:"storage_class"`
	// Per-field: an install may pin the bucket in env and leave credentials to the UI, or the reverse.
	S3Endpoint  bool `json:"s3_endpoint"`
	S3Bucket    bool `json:"s3_bucket"`
	S3Region    bool `json:"s3_region"`
	S3AccessKey bool `json:"s3_access_key"`
	S3SecretKey bool `json:"s3_secret_key"`
	S3ForcePath bool `json:"s3_force_path_style"`
}

// Any reports whether the environment pins anything at all.
func (l Locks) Any() bool {
	return l.Enabled || l.Host || l.Storage || l.StorageClass || l.S3Endpoint || l.S3Bucket ||
		l.S3Region || l.S3AccessKey || l.S3SecretKey || l.S3ForcePath
}

// Locks returns the environment-pinned fields for this install.
func (s *Service) Locks() Locks {
	c := s.cfg
	return Locks{
		Enabled:      c.EnabledSet,
		Host:         strings.TrimSpace(c.Host) != "",
		Storage:      strings.TrimSpace(c.StorageType) != "",
		StorageClass: strings.TrimSpace(c.StorageClass) != "",
		S3Endpoint:   c.S3Endpoint != "",
		S3Bucket:     c.S3Bucket != "",
		S3Region:     c.S3Region != "",
		S3AccessKey:  c.S3AccessKey != "",
		S3SecretKey:  c.S3SecretKey != "",
		S3ForcePath:  c.S3ForcePath,
	}
}

// Save skips env-pinned fields; validating a change (entitlement, host, stranded blobs) is the caller's job.
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
	if in.StorageClass != nil && !locks.StorageClass {
		st.StorageClass = models.NormalizeRegistryStorageClass(*in.StorageClass)
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
	// The API never returns the secret, so a blank means keep what is stored.
	if in.S3SecretKey != nil && !locks.S3SecretKey && strings.TrimSpace(*in.S3SecretKey) != "" {
		enc, eErr := crypto.Encrypt(strings.TrimSpace(*in.S3SecretKey))
		if eErr != nil {
			return nil, fmt.Errorf("encrypt S3 secret key: %w", eErr)
		}
		st.S3SecretKeyEnc = enc
	}

	st.VolumeName = st.DataVolume()
	st.DeleteEnabled = in.DeleteEnabled
	st.PerWorkspaceQuotaMB = in.PerWorkspaceQuotaMB

	if err := s.repo.Upsert(st); err != nil {
		return nil, err
	}
	// Return the env-layered view so the caller renders the effective configuration.
	s.applyEnvConfig(st)
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	return st, nil
}

// Silent by design since it runs on every read; config.validateRegistry flags bad values once at boot.
func normalizeStorage(t string) string {
	if strings.ToLower(strings.TrimSpace(t)) == models.RegistryStorageS3 {
		return models.RegistryStorageS3
	}
	return models.RegistryStorageFilesystem
}

// A malformed host is dropped: matching nothing would leave the tenant check silently inert.
func (s *Service) envHost() string {
	host, err := NormalizeHost(s.cfg.Host)
	if err != nil {
		logger.Error("registry: ignoring invalid MIABI_REGISTRY_HOST", "error", err)
		return ""
	}
	return host
}

// Env wins only where it speaks, so declared installs stay authoritative and others are configured from the UI.
func (s *Service) applyEnvConfig(st *models.RegistrySettings) {
	c := s.cfg
	// Only an explicitly set variable pins enablement; absent leaves it to the admin UI.
	if c.EnabledSet {
		st.Enabled = c.Enabled
	}
	if host := s.envHost(); host != "" {
		st.Host = host
	}
	if c.StorageType != "" {
		st.StorageType = normalizeStorage(c.StorageType)
	} else {
		// Stored values may predate driver names; an unrecognized one must not read as "not filesystem".
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
	if c.StorageClass != "" {
		st.StorageClass = c.StorageClass
	}
	st.StorageClass = models.NormalizeRegistryStorageClass(st.StorageClass)
	st.VolumeName = st.DataVolume()
}

// The class must exist, be enabled, and live on the control-plane node (or be shared).
func (s *Service) ValidateStorageClass(name string) error {
	name = models.NormalizeRegistryStorageClass(name)
	if name == "" {
		return nil
	}
	if s.classes == nil {
		return errors.New("storage classes are unavailable")
	}
	_, err := s.classes.Resolve(s.classes.LocalID(), name)
	return err
}

// The registry volume is not a workspace volume, so the class's own count misses it.
func (s *Service) StorageClassUser(name string) string {
	st, err := s.Get()
	if err != nil || st.UsesS3() || st.StorageClass == "" || st.StorageClass != name {
		return ""
	}
	return "the built-in container registry"
}

func (s *Service) StorageSource(st *models.RegistrySettings) string {
	if strings.TrimSpace(s.cfg.StorageType) != "" {
		return "env"
	}
	if st != nil && st.UsesS3() {
		return "stored"
	}
	return "default"
}

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

// HostFor validates both candidates; an unusable host resolves to "" so distribution reports why.
func (s *Service) HostFor(st *models.RegistrySettings) string {
	if host := s.envHost(); host != "" {
		return host
	}
	// Legacy stored host, still honored so upgraded installs keep serving the name their images reference; read-only now.
	if host, err := NormalizeHost(st.Host); err == nil && host != "" {
		return host
	}
	if s.external == nil {
		return ""
	}
	base, _ := s.external.ExternalAccess(models.DefaultClusterID)
	if base = strings.TrimSpace(base); base == "" {
		return ""
	}
	host, err := NormalizeHost("registry." + base)
	if err != nil {
		logger.Error("registry: the default cluster's external domain does not yield a usable registry host", "base_domain", base, "error", err)
		return ""
	}
	return host
}

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

// readonly lets the registry keep serving pulls while garbage-collect runs on the same storage.
func (s *Service) renderEnv(st *models.RegistrySettings, readonly bool) ([]string, error) {
	env := []string{fmt.Sprintf("REGISTRY_HTTP_ADDR=:%d", Port)}
	if st.DeleteEnabled {
		env = append(env, "REGISTRY_STORAGE_DELETE_ENABLED=true")
	}
	if readonly {
		env = append(env, "REGISTRY_STORAGE_MAINTENANCE_READONLY_ENABLED=true")
	}
	if st.UsesS3() {
		// Defense in depth: no path may render an S3 config this install is not licensed for.
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

// Ensure is idempotent and tears down when disabled.
func (s *Service) Ensure(ctx context.Context, dc docker.Client) error {
	st, err := s.Get()
	if err != nil {
		return err
	}
	if !st.Enabled {
		s.warnIfDisabledByLock()
		return s.Teardown(ctx, dc)
	}
	// Tear down unlicensed storage: a lapsed install may already be serving from the bucket; data is untouched.
	if reason := s.StorageUnavailableReason(st); reason != "" {
		logger.Error("internal registry not started: " + reason)
		if err := s.Teardown(ctx, dc); err != nil {
			logger.Warn("registry: teardown after storage check failed", "error", err)
		}
		return errors.New(reason)
	}
	if s.HostFor(st) == "" {
		// The gateway route terminates TLS and enforces forwardAuth and needs a hostname; otherwise pulls just fail.
		logger.Error("internal registry is enabled but has no usable hostname — set MIABI_REGISTRY_HOST or the default cluster's external domain; no gateway route is published until then")
	}
	if err := s.startContainer(ctx, dc, st, false); err != nil {
		return err
	}
	// Best-effort: the container is up regardless of a gateway hiccup.
	if s.proxy != nil {
		if err := s.proxy.SyncRegistry(ctx, s.proxyConfig(st, true)); err != nil {
			logger.Warn("registry: seed gateway route failed", "error", err)
		}
	}
	logger.Info("internal registry ready", "image", s.image(), "storage", st.StorageType, "host", s.HostFor(st))
	return nil
}

// Silently honoring an explicit env false leaves the console showing a registry that never comes up.
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

// volumeMounts is the registry's data-volume mount (filesystem driver only).
func (s *Service) volumeMounts(st *models.RegistrySettings) map[string]string {
	if st.UsesS3() {
		return map[string]string{}
	}
	return map[string]string{st.DataVolume(): dataPath}
}

// An existing volume is reused, so a later-disabled class keeps serving what it holds.
func (s *Service) ensureDataVolume(ctx context.Context, dc docker.Client, st *models.RegistrySettings) error {
	name := st.DataVolume()
	if _, err := dc.InspectVolume(ctx, name); err == nil {
		return nil
	} else if !errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("inspect registry volume %q: %w", name, err)
	}
	spec := docker.VolumeSpec{Name: name, Labels: docker.PlatformLabels(docker.RoleRegistry, docker.ManagedByMiabi, nil)}
	if st.StorageClass != "" {
		// Refuse rather than fall back: blobs pushed to the data root would vanish once the class returns.
		if s.classes == nil {
			return fmt.Errorf("registry storage class %q: storage classes are unavailable", st.StorageClass)
		}
		class, err := s.classes.Resolve(s.classes.LocalID(), st.StorageClass)
		if err != nil {
			return fmt.Errorf("registry storage class: %w", err)
		}
		if err := s.classes.EnsureDir(ctx, class, name); err != nil {
			return fmt.Errorf("registry storage class %q: %w", class.Name, err)
		}
		spec.DevicePath = class.VolumePath(name)
	}
	if _, err := dc.CreateVolumeWith(ctx, spec); err != nil {
		return fmt.Errorf("ensure registry volume %q: %w", name, err)
	}
	return nil
}

func (s *Service) startContainer(ctx context.Context, dc docker.Client, st *models.RegistrySettings, readonly bool) error {
	nets, err := s.networks(ctx, dc)
	if err != nil {
		return err
	}
	mounts := s.volumeMounts(st)
	if !st.UsesS3() {
		if err := s.ensureDataVolume(ctx, dc, st); err != nil {
			return err
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
	// Written before the container starts: distribution reads the htpasswd file at boot.
	if err := s.ensureUpstreamAuth(ctx, dc, img); err != nil {
		return err
	}
	env = append(env, upstreamAuthEnv()...)
	mounts[authVolume] = authPath
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

// The old shared-network fallback let any app container pull any workspace's images without a credential.
var ErrNoInternalNetwork = errors.New(
	"refusing to start the internal registry: it has no private network to join, and the shared proxy " +
		"network is not a substitute — it is reachable by every app container. Add the platform's private " +
		"network to your stack (labelled " + docker.LabelRole + "=" + docker.RolePlatformInternal + "), or " +
		"name it explicitly with MIABI_INTERNAL_NETWORK")

// Discovered by label when MIABI_INTERNAL_NETWORK is unset; nothing found means not starting (see ErrNoInternalNetwork).
func (s *Service) networks(ctx context.Context, dc docker.Client) ([]string, error) {
	name := s.internalNetwork
	if name == "" {
		name = discoverInternalNetwork(ctx, dc)
	}
	if name == "" {
		return nil, ErrNoInternalNetwork
	}
	if _, err := dc.EnsureNetwork(ctx, name); err != nil {
		return nil, fmt.Errorf("ensure network %q: %w", name, err)
	}
	return []string{name}, nil
}

// A listing failure counts as "none" rather than falling back to a shared fabric.
func discoverInternalNetwork(ctx context.Context, dc docker.Client) string {
	nets, err := dc.ListNetworks(ctx)
	if err != nil {
		return ""
	}
	for _, n := range nets {
		if role, _ := docker.LabelValue(n.Labels, docker.LabelRole); role == docker.RolePlatformInternal {
			return n.Name
		}
	}
	return ""
}

// Distribution v3 (registry:3) moved the default config from /etc/docker/registry to /etc/distribution.
const gcScript = `cfg=/etc/distribution/config.yml; [ -f "$cfg" ] || cfg=/etc/docker/registry/config.yml; exec registry garbage-collect "$cfg"`

// GarbageCollect flips the registry read-only (pulls work, pushes pause) while collecting; needs deletes enabled.
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

	// Read-only so no writes race the collector.
	if err := s.startContainer(ctx, dc, st, true); err != nil {
		return fmt.Errorf("registry gc: enter read-only: %w", err)
	}
	// Always restore read-write, even if GC fails.
	defer func() {
		if err := s.startContainer(ctx, dc, st, false); err != nil {
			logger.Error("registry gc: failed to restore read-write — registry left read-only", "error", err)
		}
	}()

	// Override the entrypoint, which would otherwise `serve`.
	env, err := s.renderEnv(st, false)
	if err != nil {
		return err
	}
	code, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:       ContainerName + "-gc",
		Image:      s.image(),
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{gcScript},
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

// Teardown keeps data volumes.
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

func (s *Service) authURL() string {
	base := strings.TrimRight(firstNonEmpty(s.cfg.AuthURL, s.controlURL), "/")
	if base == "" {
		return ""
	}
	return base + "/internal/registry/auth"
}

// SyncGateway lets domain or certificate provider changes apply without a restart.
func (s *Service) SyncGateway(ctx context.Context) error {
	if s.proxy == nil {
		return nil
	}
	st, err := s.Get()
	if err != nil {
		return err
	}
	if !st.Enabled || s.StorageUnavailableReason(st) != "" {
		return nil
	}
	return s.proxy.SyncRegistry(ctx, s.proxyConfig(st, true))
}

func (s *Service) certProvider() string {
	if s.external == nil {
		return ""
	}
	_, provider := s.external.ExternalAccess(models.DefaultClusterID)
	return provider
}

func (s *Service) proxyConfig(st *models.RegistrySettings, enabled bool) proxy.RegistryProxy {
	return proxy.RegistryProxy{
		Enabled:      enabled,
		Host:         s.HostFor(st),
		Upstream:     fmt.Sprintf("http://%s:%d", Alias, Port),
		AuthURL:      s.authURL(),
		TLSProvider:  s.certProvider(),
		UpstreamAuth: s.upstreamAuthHeader(),
		// Off only behind a TLS terminator with no trusted proxies configured, where the redirect would loop.
		HTTPSRedirect: s.cfg.HTTPSRedirect,
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
