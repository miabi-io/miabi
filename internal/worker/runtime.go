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

package worker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/hostmount"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/netalloc"
	"github.com/miabi-io/miabi/internal/services/network"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Optional; nil leaves references untouched.
type SecretResolver interface {
	ResolveAll(workspaceID uint, env []string) ([]string, error)
	CredentialSecret(workspaceID uint, enc, ref string) (string, error)
	ValueByName(workspaceID uint, name string) (string, bool)
}

// A vault reference with no resolver errors rather than pulling anonymously and failing as "manifest unknown".
func (b *runtimeBuilder) credentialSecret(workspaceID uint, enc, ref string) (string, error) {
	if b.secrets == nil {
		if ref != "" {
			return "", fmt.Errorf("credential references secret %q but the vault is unavailable", ref)
		}
		if enc == "" {
			return "", nil
		}
		return crypto.Decrypt(enc)
	}
	return b.secrets.CredentialSecret(workspaceID, enc, ref)
}

// User and Restricted are independent: the profile hardens the container whichever non-root user it runs as.
type Security struct {
	User            string // "uid:gid" the container runs as; "" = image default
	NoNewPrivileges bool
	CapDrop         []string
	// CapAdd/Devices are the app's additive grants; the workspace profile is subtractive.
	CapAdd  []string
	Devices []string
	// ReadOnlyRootfs is the app's own. The app's hardening also joins NoNewPrivileges and CapDrop.
	ReadOnlyRootfs bool
	// Restricted marks the non-root mandate, not merely the presence of a user.
	Restricted bool
}

// HasUser triggers making the managed volumes writable by the pinned account.
func (s Security) HasUser() bool { return s.User != "" }

func (s Security) applyTo(spec *docker.RunSpec) {
	spec.User = s.User
	spec.NoNewPrivileges = s.NoNewPrivileges
	spec.CapDrop = s.CapDrop
	spec.CapAdd = s.CapAdd
	spec.Devices = s.Devices
	spec.ReadOnlyRootfs = s.ReadOnlyRootfs
}

// A plan may exempt official-template apps from the restricted UID so they keep the image's own user.
type SecurityResolver interface {
	ContainerSecurity(workspaceID uint, officialTemplate bool) Security
}

// SecurityFunc adapts a plain function to SecurityResolver.
type SecurityFunc func(workspaceID uint, officialTemplate bool) Security

// ContainerSecurity implements SecurityResolver.
func (f SecurityFunc) ContainerSecurity(id uint, officialTemplate bool) Security {
	if f == nil {
		return Security{}
	}
	return f(id, officialTemplate)
}

// Shared by deploys and one-off Jobs so a Job's environment can never drift from the real deploy.
type runtimeBuilder struct {
	stackEnv *repositories.StackEnvVarRepository
	routes   *repositories.RouteRepository
	secrets  SecretResolver
	// securityInitImage chowns a restricted app's managed volumes to the platform UID.
	security          SecurityResolver
	securityInitImage string
	// Remote nodes get the same subnet as the control plane; nil = Docker default.
	alloc *netalloc.Service
	// In cluster mode routed apps join the ingress overlay so the gateway reaches them without a host port.
	cluster ClusterCap
	// nil in processes that never deploy.
	configs ConfigProjector
	// A workspace can lose its privileged flag and never be written to again; nil = no re-check.
	grants GrantGuard
}

// GrantGuard reports whether a workspace may still hold a grant of the given tier.
type GrantGuard interface {
	GrantAllowed(workspaceID uint, tier models.CapabilityTier) error
}

// GrantGuardFunc adapts a plain function to GrantGuard.
type GrantGuardFunc func(workspaceID uint, tier models.CapabilityTier) error

// GrantAllowed implements GrantGuard.
func (f GrantGuardFunc) GrantAllowed(workspaceID uint, tier models.CapabilityTier) error {
	if f == nil {
		return nil
	}
	return f(workspaceID, tier)
}

type ConfigProjector interface {
	Get(workspaceID, id uint) (*models.Config, error)
	Project(cfg *models.Config, mount models.AppMount) ([]config.ProjectedFile, error)
	ProjectWith(cfg *models.Config, mount models.AppMount, r config.Resolver) ([]config.ProjectedFile, error)
}

// ClusterCap is the per-cluster control path. Implemented by services/cluster.
type ClusterCap interface {
	IsSwarm(clusterID uint) bool
	Manager(ctx context.Context, clusterID uint) (docker.Client, error)
	WorkspaceOverlay(clusterID uint, n models.Network) bool
	EnsureWorkspaceOverlay(ctx context.Context, clusterID uint, n models.Network) error
	ServiceEndpointMode(clusterID uint) models.ServiceEndpointMode
}

func (b *runtimeBuilder) SetCluster(c ClusterCap) { b.cluster = c }

// SetConfigs supplies the projector that turns config mounts into files.
func (b *runtimeBuilder) SetConfigs(c ConfigProjector) { b.configs = c }

func (b *runtimeBuilder) isSwarm(clusterID uint) bool {
	return b.cluster != nil && b.cluster.IsSwarm(clusterID)
}

func (b *runtimeBuilder) SetAllocator(a *netalloc.Service) { b.alloc = a }

func (b *runtimeBuilder) SetSecurity(r SecurityResolver, initImage string) {
	b.security = r
	b.securityInitImage = initImage
}

func (b *runtimeBuilder) containerSecurity(app *models.Application) Security {
	return b.containerSecurityFor(app, app.OfficialTemplate)
}

// A caller running something other than the template's own image can decline the exemption.
func (b *runtimeBuilder) containerSecurityFor(app *models.Application, officialTemplate bool) Security {
	if b.security == nil {
		return Security{}
	}
	return b.security.ContainerSecurity(app.WorkspaceID, officialTemplate)
}

// Deploy-time backstop for workloads configured before the policy tightened.
var ErrRunAsUserForbidden = errors.New("the restricted security profile requires a non-root numeric uid")

// Under the restricted profile the chosen user replaces the platform UID but must still be non-root.
func (s Security) withRunAsUser(runAsUser string) (Security, error) {
	v, err := models.NormalizeRunAsUser(runAsUser)
	if err != nil {
		return s, err
	}
	if v == "" {
		return s, nil
	}
	if s.Restricted && !models.RunAsUserIsNonRoot(v) {
		return s, fmt.Errorf("%w (got %q)", ErrRunAsUserForbidden, v)
	}
	s.User = v
	return s, nil
}

// SetGrantGuard wires the deploy-time re-check of capability and device grants.
func (b *runtimeBuilder) SetGrantGuard(g GrantGuard) { b.grants = g }

func (b *runtimeBuilder) workloadSecurity(app *models.Application, runAsUser string) (Security, error) {
	return b.workloadSecurityFor(app, runAsUser, app.OfficialTemplate)
}

// A job running its own image must not inherit the template image's exemption, or it could run as root.
func (b *runtimeBuilder) workloadSecurityFor(app *models.Application, runAsUser string, officialTemplate bool) (Security, error) {
	sec, err := b.containerSecurityFor(app, officialTemplate).withRunAsUser(runAsUser)
	if err != nil {
		return sec, err
	}
	if sec, err = b.withGrants(sec, app); err != nil {
		return sec, err
	}
	return sec.withHardening(app), nil
}

// Both only take privileges away, so they combine rather than one replacing the other.
func (s Security) withHardening(app *models.Application) Security {
	s.NoNewPrivileges = s.NoNewPrivileges || app.NoNewPrivileges
	s.ReadOnlyRootfs = app.ReadOnlyRootFilesystem
	if len(app.DropCapabilities) == 0 {
		return s
	}
	drop := append([]string(nil), s.CapDrop...)
	for _, c := range app.DropCapabilities {
		if !containsString(drop, c) {
			drop = append(drop, c)
		}
	}
	s.CapDrop = drop
	return s
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// A grant that outlived its authorisation stops the deploy.
func (b *runtimeBuilder) withGrants(sec Security, app *models.Application) (Security, error) {
	if len(app.AddCapabilities) == 0 && len(app.Devices) == 0 {
		return sec, nil
	}
	if sec.Restricted {
		return sec, models.ErrCapabilityRestricted
	}
	if b.grants != nil {
		tier := models.HighestCapabilityTier(app.AddCapabilities)
		if dt := models.HighestDeviceTier(app.Devices); dt > tier {
			tier = dt
		}
		if err := b.grants.GrantAllowed(app.WorkspaceID, tier); err != nil {
			return sec, err
		}
	}
	sec.CapAdd = app.AddCapabilities
	sec.Devices = app.Devices
	return sec, nil
}

// Docker seeds an empty volume with the image's content and ownership, so the chown runs with the app image.
func (b *runtimeBuilder) prepareVolumeOwnership(ctx context.Context, eng docker.Client, sec Security, image string, mounts map[string]string) error {
	if !sec.HasUser() || len(mounts) == 0 {
		return nil
	}
	paths := make([]string, 0, len(mounts))
	for _, p := range mounts {
		paths = append(paths, p)
	}
	chown := append([]string{"chown", "-R", sec.User}, paths...)

	// The chown runs with the app image so the volume is seeded from it before ownership is fixed.
	if image != "" {
		if exit, out, err := eng.RunOneShot(ctx, docker.RunSpec{Image: image, Entrypoint: chown, Mounts: mounts}); err == nil && exit == 0 {
			return nil
		} else {
			// The app-image mount has still seeded the volume; only the chown failed.
			logger.Debug("volume ownership: app-image chown unavailable, using init image",
				"image", image, "exit", exit, "error", err, "output", strings.TrimSpace(out))
		}
	}
	if b.securityInitImage == "" {
		return nil // no fallback tool image configured; best effort
	}
	if err := eng.PullImage(ctx, b.securityInitImage, nil); err != nil {
		return fmt.Errorf("pull security-init image %q: %w", b.securityInitImage, err)
	}
	exit, out, err := eng.RunOneShot(ctx, docker.RunSpec{Image: b.securityInitImage, Entrypoint: chown, Mounts: mounts})
	if err != nil {
		return fmt.Errorf("chown volumes: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("chown volumes exited %d: %s", exit, strings.TrimSpace(out))
	}
	return nil
}

type RuntimeContext struct {
	Env         []string
	Networks    []string
	Mounts      map[string]string
	Binds       []docker.BindMount
	Files       []docker.FileEntry
	MemoryBytes int64
	NanoCPUs    int64
}

// eng must be the Docker client for the app's node.
func (b *runtimeBuilder) buildRuntimeContext(ctx context.Context, eng docker.Client, app *models.Application) (RuntimeContext, error) {
	if err := b.syncNetworks(ctx, eng, app); err != nil {
		return RuntimeContext{}, err
	}
	// Only route-exposed apps join the shared proxy network; route changes reconcile running containers live.
	var networks []string
	if b.appHasRoutes(app.ID) {
		networks = append(networks, node.AppNetwork)
		// Only the globally-unique upstream alias is registered on the overlay, never the workspace-scoped app.Name.
		if b.isSwarm(app.ClusterID) {
			networks = append(networks, node.IngressOverlay)
		}
	}
	for _, n := range app.Networks {
		networks = append(networks, n.DockerName)
	}
	// The app's own alias is not registered here: a one-off Job must not impersonate the service.
	if app.Stack != nil && app.Stack.DockerNetwork != "" {
		if _, err := eng.EnsureNetwork(ctx, app.Stack.DockerNetwork); err == nil {
			networks = append(networks, app.Stack.DockerNetwork)
		}
	}
	mounts := make(map[string]string, len(app.Mounts))
	var binds []docker.BindMount
	for _, m := range app.Mounts {
		// Resolve the source path from the server-owned allow-list, never from stored or client input.
		if m.HostPreset != "" {
			p, ok := hostmount.Get(m.HostPreset)
			if !ok {
				continue // preset removed from the allow-list: drop it, never bind an unknown path
			}
			target := m.Path
			if target == "" {
				target = p.DefaultTarget
			}
			binds = append(binds, docker.BindMount{Source: p.Source, Target: target, ReadOnly: m.ReadOnly && p.AllowReadOnly})
			continue
		}
		if m.HostPath != "" {
			binds = append(binds, docker.BindMount{Source: m.HostPath, Target: m.Path, ReadOnly: m.ReadOnly})
			continue
		}
		if m.ConfigID != 0 {
			continue // projected as files below, never as a directory bind
		}
		mounts[m.DockerName] = m.Path
	}
	env, err := b.buildEnv(app)
	if err != nil {
		return RuntimeContext{}, err
	}
	files, ferr := b.projectConfigs(app, b.configResolver(app, env))
	if ferr != nil {
		return RuntimeContext{}, ferr
	}
	return RuntimeContext{
		Env:         env,
		Networks:    networks,
		Mounts:      mounts,
		Binds:       binds,
		Files:       files,
		MemoryBytes: app.MemoryBytes,
		NanoCPUs:    app.NanoCPUs,
	}, nil
}

// Vault first, then the app's env, so a value moved into the vault needs no config edit.
func (b *runtimeBuilder) configResolver(app *models.Application, env []string) config.Resolver {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok {
			values[k] = v
		}
	}
	return config.ResolverFunc{
		SecretFn: func(name string) (string, bool) {
			if b.secrets == nil {
				return "", false
			}
			return b.secrets.ValueByName(app.WorkspaceID, name)
		},
		EnvFn: func(name string) (string, bool) {
			v, ok := values[name]
			return v, ok
		},
	}
}

func (b *runtimeBuilder) projectConfigs(app *models.Application, r config.Resolver) ([]docker.FileEntry, error) {
	if b.configs == nil {
		return nil, nil
	}
	var out []docker.FileEntry
	for _, m := range app.Mounts {
		if m.ConfigID == 0 {
			continue
		}
		cfg, err := b.configs.Get(app.WorkspaceID, m.ConfigID)
		if err != nil {
			continue
		}
		files, perr := b.configs.ProjectWith(cfg, m, r)
		if perr != nil {
			return nil, fmt.Errorf("project config %s: %w", cfg.Name, perr)
		}
		for _, f := range files {
			out = append(out, docker.FileEntry{Path: f.Path, Content: f.Content, Mode: f.Mode})
		}
	}
	return out, nil
}

func (b *runtimeBuilder) appHasRoutes(appID uint) bool {
	if b.routes == nil {
		return false
	}
	n, err := b.routes.CountByApp(appID)
	return err == nil && n > 0
}

// Workspace networks are created on the control plane, so a remote node may lack them until recreated here.
func (b *runtimeBuilder) syncNetworks(ctx context.Context, eng docker.Client, app *models.Application) error {
	if _, err := eng.EnsureNetwork(ctx, node.AppNetwork); err != nil {
		return fmt.Errorf("ensure gateway network: %w", err)
	}
	if len(app.Networks) == 0 {
		return nil
	}
	existing, err := eng.ListNetworks(ctx)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(existing))
	for _, n := range existing {
		have[n.Name] = true
	}
	for _, n := range app.Networks {
		if have[n.DockerName] {
			continue
		}
		// An overlay is swarm-scoped: a worker cannot create one, and Docker materializes it on first attachment.
		if b.cluster != nil && b.cluster.WorkspaceOverlay(app.ClusterID, n) {
			if err := b.cluster.EnsureWorkspaceOverlay(ctx, app.ClusterID, n); err != nil {
				return err
			}
			continue
		}
		if n.Driver == network.DriverOverlay {
			continue
		}
		spec := docker.NetworkSpec{Name: n.DockerName, Driver: n.Driver, Internal: n.Internal}
		if b.alloc != nil {
			// Reuse the network's pool subnet so it is identical on every node.
			if _, _, err := b.alloc.EnsureManaged(ctx, eng, spec, app.ServerID, models.NetAllocKindWorkspace); err != nil {
				return fmt.Errorf("create network %s: %w", n.DockerName, err)
			}
			continue
		}
		if _, err := eng.CreateNetworkSpec(ctx, spec); err != nil {
			return fmt.Errorf("create network %s: %w", n.DockerName, err)
		}
	}
	return nil
}

// App-level keys override stack defaults; an unknown secret reference fails rather than injecting blank.
func (b *runtimeBuilder) buildEnv(app *models.Application) ([]string, error) {
	merged := map[string]string{}
	var order []string
	add := func(key, val string) {
		if _, seen := merged[key]; !seen {
			order = append(order, key)
		}
		merged[key] = val
	}
	if app.StackID != nil && b.stackEnv != nil {
		if shared, err := b.stackEnv.ListByStack(*app.StackID); err == nil {
			for _, v := range shared {
				add(v.Key, decryptIfSecret(v.Value, v.IsSecret))
			}
		}
	}
	for _, v := range app.EnvVars {
		add(v.Key, decryptIfSecret(v.Value, v.IsSecret))
	}
	env := make([]string, 0, len(order))
	for _, k := range order {
		env = append(env, k+"="+merged[k])
	}
	if b.secrets != nil {
		return b.secrets.ResolveAll(app.WorkspaceID, env)
	}
	return env, nil
}

func decryptIfSecret(value string, isSecret bool) string {
	if isSecret {
		if dec, err := crypto.Decrypt(value); err == nil {
			return dec
		}
	}
	return value
}

// Object names embed the file digest, so a content change swaps the immutable config and rolls the service.
func (b *runtimeBuilder) publishConfigs(ctx context.Context, eng docker.Client, app *models.Application, workspaceScope string, env []string) ([]docker.ServiceConfig, error) {
	if b.configs == nil {
		return nil, nil
	}
	var out []docker.ServiceConfig
	for _, m := range app.Mounts {
		if m.ConfigID == 0 {
			continue
		}
		cfg, err := b.configs.Get(app.WorkspaceID, m.ConfigID)
		if err != nil {
			continue
		}
		files, perr := b.configs.ProjectWith(cfg, m, b.configResolver(app, env))
		if perr != nil {
			return nil, fmt.Errorf("project config %s: %w", cfg.Name, perr)
		}
		for _, f := range files {
			obj := docker.ConfigObject{
				Workspace: workspaceScope,
				Config:    cfg.Name,
				Key:       f.Path,
				Digest:    f.FileDigest()[:12],
				Content:   f.Content,
			}
			id, cerr := eng.EnsureConfig(ctx, obj)
			if cerr != nil {
				return nil, cerr
			}
			out = append(out, docker.ServiceConfig{
				ID: id, Name: obj.ObjectName(), Path: f.Path, Mode: parseFileMode(f.Mode),
			})
		}
	}
	return out, nil
}

// An unparseable mode falls back to 0644 rather than failing a deploy.
func parseFileMode(mode string) uint32 {
	if mode == "" {
		return 0o644
	}
	v, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return 0o644
	}
	return uint32(v) & 0o777
}
