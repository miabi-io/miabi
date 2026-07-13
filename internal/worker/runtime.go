// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/hostmount"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/netalloc"
	"github.com/miabi-io/miabi/internal/services/network"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// SecretResolver substitutes `${{ secrets.NAME }}` references in env values with
// the workspace's stored secret values. Implemented by the secret service;
// optional (nil = references are left untouched).
type SecretResolver interface {
	ResolveAll(workspaceID uint, env []string) ([]string, error)
}

// Security is the resolved container hardening for a workspace's application and
// job containers under the "restricted" security profile. The zero value (empty
// User) means no restriction — run as the image's default user.
type Security struct {
	User            string // "uid:gid" the container runs as; "" = image default
	NoNewPrivileges bool
	CapDrop         []string
}

// Restricted reports whether any hardening applies.
func (s Security) Restricted() bool { return s.User != "" }

// applyTo stamps the resolved hardening onto a RunSpec.
func (s Security) applyTo(spec *docker.RunSpec) {
	spec.User = s.User
	spec.NoNewPrivileges = s.NoNewPrivileges
	spec.CapDrop = s.CapDrop
}

// SecurityResolver resolves the security profile for a workspace's app/job
// containers. Implemented by an adapter over the quota service + platform config;
// optional (nil = no restriction, today's behavior). officialTemplate marks an
// app created from an official marketplace template, which a plan may exempt from
// the restricted UID so it can keep the image's own default user.
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

// runtimeBuilder assembles the runtime substrate (env, networks, mounts, limits)
// a container runs with in an application's context. Shared by the deploy
// pipeline and one-off Jobs so a Job's environment can never drift from the real
// deploy.
type runtimeBuilder struct {
	stackEnv *repositories.StackEnvVarRepository
	routes   *repositories.RouteRepository
	secrets  SecretResolver
	// security resolves the restricted-profile hardening for a workspace; nil =
	// no restriction. securityInitImage is the tiny image used to chown a
	// restricted app's managed volumes to the platform UID.
	security          SecurityResolver
	securityInitImage string
	// alloc carves a pool subnet when recreating a workspace network on a node so
	// remote nodes get the same subnet as the control plane (nil = Docker default).
	alloc *netalloc.Service
	// cluster reports whether the manager is a swarm manager. In cluster mode a
	// routed app also joins the shared ingress overlay, so the central gateway can
	// reach it on any node without a published host port (nil = never cluster).
	cluster ClusterCap
}

// ClusterCap reports whether the manager engine is a reachable swarm manager.
// Implemented by services/cluster.
type ClusterCap interface {
	CapCluster() bool
}

// SetCluster wires swarm detection (nil-safe). Shared by the deploy and job
// handlers (both embed *runtimeBuilder).
func (b *runtimeBuilder) SetCluster(c ClusterCap) { b.cluster = c }

func (b *runtimeBuilder) clusterOn() bool { return b.cluster != nil && b.cluster.CapCluster() }

// SetAllocator wires the subnet allocator used when recreating networks on a
// node (nil-safe). Shared by the deploy and job handlers.
func (b *runtimeBuilder) SetAllocator(a *netalloc.Service) { b.alloc = a }

// SetSecurity wires the container security resolver and the volume-chown init
// image. Shared by the deploy and job handlers (both embed *runtimeBuilder).
func (b *runtimeBuilder) SetSecurity(r SecurityResolver, initImage string) {
	b.security = r
	b.securityInitImage = initImage
}

// containerSecurity resolves the hardening for an app's workspace (nil-safe).
func (b *runtimeBuilder) containerSecurity(app *models.Application) Security {
	if b.security == nil {
		return Security{}
	}
	return b.security.ContainerSecurity(app.WorkspaceID, app.OfficialTemplate)
}

// prepareRestrictedVolumes makes an app's managed volume mounts writable by the
// restricted-profile non-root UID (UID:0). No-op unless the profile is restricted
// and the app has managed mounts. Runs before the real container starts;
// idempotent across deploys.
//
// The subtlety: Docker seeds an empty named volume with the image's content — and
// the image's file ownership — the first time any container mounts it. A plain
// busybox `chown` run before the app starts is therefore defeated for images that
// bake data into the mount path (WordPress, Ghost, nginx, …): the app's own first
// mount re-copies that data as root and the non-root process then can't write it.
//
// So the chown is run with the *app image* itself. That one-shot's mount performs
// the seed copy, and the chown then fixes ownership of the seeded data, leaving a
// populated, correctly-owned volume the real container mounts without re-copying.
// Images that lack a chown/shell (distroless, scratch) fall back to the busybox
// init image — by then the app-image mount has already seeded the volume, so the
// busybox chown only has to correct ownership of the already-seeded data.
func (b *runtimeBuilder) prepareRestrictedVolumes(ctx context.Context, eng docker.Client, sec Security, image string, mounts map[string]string) error {
	if !sec.Restricted() || len(mounts) == 0 {
		return nil
	}
	paths := make([]string, 0, len(mounts))
	for _, p := range mounts {
		paths = append(paths, p)
	}
	chown := append([]string{"chown", "-R", sec.User}, paths...)

	// Preferred: chown with the app image, so the volume is seeded from the correct
	// image before ownership is fixed. The image is already present (the deploy/job
	// pulled it before this step).
	if image != "" {
		if exit, out, err := eng.RunOneShot(ctx, docker.RunSpec{Image: image, Entrypoint: chown, Mounts: mounts}); err == nil && exit == 0 {
			return nil
		} else {
			// The app-image mount has still seeded the volume; it just couldn't chown
			// (no chown binary). Fall through to the busybox init image to fix ownership.
			logger.Debug("restricted volumes: app-image chown unavailable, using init image",
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

// RuntimeContext is the substrate common to any container run in an app's
// environment: resolved env, the networks to join (already ensured to exist on
// the node), volume mounts, and resource limits. Callers layer run-specific
// concerns (image, command, ports, aliases, healthcheck, labels) on top.
type RuntimeContext struct {
	Env         []string
	Networks    []string
	Mounts      map[string]string
	Binds       []docker.BindMount
	MemoryBytes int64
	NanoCPUs    int64
}

// buildRuntimeContext ensures the app's networks exist on the node (gateway +
// workspace + stack) and returns the env/networks/mounts/limits common to any
// container run for the app. eng must be the Docker client for the app's node.
func (b *runtimeBuilder) buildRuntimeContext(ctx context.Context, eng docker.Client, app *models.Application) (RuntimeContext, error) {
	if err := b.syncNetworks(ctx, eng, app); err != nil {
		return RuntimeContext{}, err
	}
	// Join the shared reverse-proxy network only while the app is exposed by a
	// route — apps with no route stay off it (less surface, cleaner isolation).
	// Adding/removing a route later reconciles a running container live.
	var networks []string
	if b.appHasRoutes(app.ID) {
		networks = append(networks, node.AppNetwork)
		// In cluster mode the central gateway reaches a routed app over the shared
		// ingress overlay instead of a published host port on its node, so the app
		// joins that overlay too. Only the globally-unique upstream alias is
		// registered (RunSpec.NetworkAliases) — never app.Name, which is
		// workspace-scoped — exactly as deployService does for service apps.
		//
		// The overlay itself is created and re-asserted by services/cluster (on
		// enable and on every refresh); a worker node could not create it anyway,
		// since an overlay is swarm-scoped.
		if b.clusterOn() {
			networks = append(networks, node.IngressOverlay)
		}
	}
	for _, n := range app.Networks {
		networks = append(networks, n.DockerName)
	}
	// Join the stack network too (so siblings resolve by service name). Unlike
	// the deploy pipeline, callers do not register the app's own alias here — a
	// one-off Job must not impersonate the service on the network.
	if app.Stack != nil && app.Stack.DockerNetwork != "" {
		if _, err := eng.EnsureNetwork(ctx, app.Stack.DockerNetwork); err == nil {
			networks = append(networks, app.Stack.DockerNetwork)
		}
	}
	mounts := make(map[string]string, len(app.Mounts))
	var binds []docker.BindMount
	for _, m := range app.Mounts {
		// Privileged host binds carry a preset key; resolve the source path from
		// the server-owned allow-list (never from stored/client input).
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
		// A host-path volume binds an operator-managed path (under /mnt/*) directly,
		// rather than mounting a Docker named volume.
		if m.HostPath != "" {
			binds = append(binds, docker.BindMount{Source: m.HostPath, Target: m.Path, ReadOnly: m.ReadOnly})
			continue
		}
		mounts[m.DockerName] = m.Path
	}
	env, err := b.buildEnv(app)
	if err != nil {
		return RuntimeContext{}, err
	}
	return RuntimeContext{
		Env:         env,
		Networks:    networks,
		Mounts:      mounts,
		Binds:       binds,
		MemoryBytes: app.MemoryBytes,
		NanoCPUs:    app.NanoCPUs,
	}, nil
}

// appHasRoutes reports whether the app is exposed by at least one route, and so
// should join the shared reverse-proxy network. Defaults to false when the route
// repository is unwired.
func (b *runtimeBuilder) appHasRoutes(appID uint) bool {
	if b.routes == nil {
		return false
	}
	n, err := b.routes.CountByApp(appID)
	return err == nil && n > 0
}

// syncNetworks makes sure the networks an app joins exist on the node it runs
// on. Workspace networks are created on the control-plane engine at creation
// time, so a remote node won't have them yet; recreate any missing ones (same
// name/driver/internal) there before the container starts. Idempotent on the
// local node, where they already exist.
func (b *runtimeBuilder) syncNetworks(ctx context.Context, eng docker.Client, app *models.Application) error {
	// The shared gateway network is a plain bridge; EnsureNetwork creates it if
	// missing.
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
		// An overlay is swarm-scoped: it is created once on the manager, and Docker
		// materializes it on this node the moment a container attaches. A worker
		// cannot create one (and must not try), so there is nothing to do here — the
		// network simply won't be listed until the first attachment.
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

// buildEnv assembles KEY=VALUE pairs, decrypting secret values and resolving
// `${{ secrets.NAME }}` Vault references. Stack-level shared vars come first,
// then the app's own vars (so an app-level key overrides the stack default). An
// unknown secret reference fails the build (the deploy/job errors out rather
// than injecting a blank value).
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
