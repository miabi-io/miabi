// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/runners"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/events"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	imagesvc "github.com/miabi-io/miabi/internal/services/image"
	"github.com/miabi-io/miabi/internal/services/network"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	runnersvc "github.com/miabi-io/miabi/internal/services/runner"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/runner/proto"
)

// DeployTopic is the eventbus topic carrying a deployment's live events.
func DeployTopic(deploymentID uint) string {
	return fmt.Sprintf("deploy:%d", deploymentID)
}

// RouteSyncer reconciles an application's reverse-proxy route. The domain
// service implements it; injected to avoid a worker->domain import.
type RouteSyncer interface {
	SyncRoute(ctx context.Context, appID uint) error
}

// DeployHandler runs the deployment pipeline: pull -> run -> health-gate ->
// swap -> release -> route sync. It is registered with the asynq worker.
type DeployHandler struct {
	*runtimeBuilder
	apps          *repositories.ApplicationRepository
	deployments   *repositories.DeploymentRepository
	releases      *repositories.ReleaseRepository
	registries    *repositories.RegistryRepository
	gitRepos      *repositories.GitRepoRepository
	portBindings  *repositories.PortBindingRepository
	volumes       *repositories.VolumeRepository
	clients       NodeDocker
	bus           *eventbus.Bus
	routes        RouteSyncer
	events        events.Recorder
	producer      *Producer
	build         buildConfig
	distributor   Distributor
	logs          *logstore.Store
	deployLock    DeployLock
	builderPolicy BuilderPolicy

	// Runner build dispatch: a git-source app's image is built on a registered
	// runner (which pushes it to the internal registry), never on this node.
	buildDispatch     BuildDispatcher
	registryHost      string        // MIABI_REGISTRY host runners push to / login
	runnerWaitTimeout time.Duration // how long a deploy waits for a runner before failing

	// GPU scheduling: capability/quota preflight + resolving a GPU request to
	// concrete devices on the app's node. Optional; nil deploys ignore GPUs.
	gpu GPUScheduler
}

// GPUScheduler validates and resolves an app's GPU request at deploy time.
// Satisfied by *gpu.Service; nil means GPU handling is disabled.
type GPUScheduler interface {
	// Preflight enforces the plan capability + workspace GPU quota (no device
	// binding). Returns nil when the app requests no GPU.
	Preflight(app *models.Application) error
	// ResolveDevices binds the app's GPU request to concrete devices on its node,
	// given the node's advertised container runtimes. Returns nil when the app
	// requests no GPU.
	ResolveDevices(ctx context.Context, app *models.Application, runtimes []string) ([]docker.GPURequest, error)
}

// SetGPU wires the GPU scheduler (optional). Without it, a GPU request on an app
// is ignored at deploy (the request-time capability gate still applies).
func (h *DeployHandler) SetGPU(g GPUScheduler) { h.gpu = g }

// ErrGPUWithCluster and ErrGPUWithRestrictedProfile are the deploy-time refusals
// for GPU requests that conflict with an incompatible runtime or security
// posture. Both fail the deploy clearly rather than silently dropping the GPU or
// the conflicting setting.
var (
	ErrGPUWithCluster           = errors.New("GPU apps must run as a single container, not a clustered (replicated) service; set the runtime to container")
	ErrGPUWithRestrictedProfile = errors.New("GPU device passthrough is incompatible with the restricted security profile; use the default profile for GPU apps")
)

// BuildDispatcher dispatches a git-source app's image build to a runner and
// returns the pushed digest. Satisfied by *runners.Dispatcher.
type BuildDispatcher interface {
	RunBuild(ctx context.Context, in runners.BuildInputs, subjectUserID uint, onLog func(string)) (string, error)
	// RunnerWaitReason explains why no runner is available for a build in the
	// workspace, surfaced in the deploy's "waiting for a runner" log.
	RunnerWaitReason(workspaceID uint) string
}

// SetBuildDispatch wires runner build dispatch for git-source app deploys: the
// registry host runners push to and the max time a deploy waits for a runner.
func (h *DeployHandler) SetBuildDispatch(d BuildDispatcher, registryHost string, runnerWaitTimeout time.Duration) {
	h.buildDispatch = d
	h.registryHost = registryHost
	h.runnerWaitTimeout = runnerWaitTimeout
}

// SetDeployLock wires the per-app deploy serialization lock (optional; nil runs
// deploys without cross-worker serialization).
func (h *DeployHandler) SetDeployLock(l DeployLock) { h.deployLock = l }

// SetLogStore wires the shared execution-log store. When set, a deployment's
// full build/deploy log is externalized to the store on terminal state and the
// DB row keeps only a bounded tail + a reference. nil keeps DB-tail-only.
func (h *DeployHandler) SetLogStore(s *logstore.Store) { h.logs = s }

// Distributor distributes built images via the internal registry so any node can
// pull them (multi-node deploys/rollbacks of Git-built apps). Satisfied by the
// registry service; nil/disabled leaves builds node-local (single-node default).
type Distributor interface {
	// DistributionEnabled reports whether the registry is enabled + configured to
	// receive build pushes.
	DistributionEnabled() bool
	// DistributionUnavailableReason returns "" when distribution is ready, else a
	// user-facing sentence naming the specific missing config.
	DistributionUnavailableReason() string
	// BuildRef is the registry ref a build is distributed under, e.g.
	// registry.<domain>/<workspace-name>/<app-name>:<deploymentID>.
	BuildRef(workspaceID uint, appName string, deploymentID uint) string
	// TagReleaseVersion adds a v<version> tag to the pushed build image (by digest)
	// so the registry mirrors the release number. Best-effort.
	TagReleaseVersion(ctx context.Context, workspaceID uint, appName, digest string, version int) error
	// IsBuildRef reports whether ref is one of this registry's distributed refs.
	IsBuildRef(ref string) bool
	// PushAuth is the credential to push/pull distributed images.
	PushAuth() *docker.RegistryAuth
}

// SetDistributor wires the internal-registry image distributor (optional).
func (h *DeployHandler) SetDistributor(d Distributor) { h.distributor = d }

// buildConfig holds the deploy's build-related dependencies now that builds run
// on runners: the platform-image resolver (for the admin-controlled buildpack
// builder policy, passed to the runner) and the image catalog (build
// provenance). Both optional.
type buildConfig struct {
	resolver ImageRefResolver
	images   *imagesvc.Service
}

// SetBuildProvenance wires the builder-image resolver (admin builder policy) and
// the image catalog (build provenance). Called after construction.
func (h *DeployHandler) SetBuildProvenance(resolver ImageRefResolver, images *imagesvc.Service) {
	h.build = buildConfig{resolver: resolver, images: images}
}

// BuilderPolicy reports whether a workspace may use a custom buildpack builder
// image. Satisfied by the quota service; nil means unrestricted.
type BuilderPolicy interface {
	CustomBuilderAllowed(workspaceID uint) bool
}

// SetBuilderPolicy wires the custom-builder capability check (defense-in-depth:
// a builder set while granted stops being honored if the capability is later
// revoked, e.g. a plan downgrade). Optional; nil honors the app's builder.
func (h *DeployHandler) SetBuilderPolicy(p BuilderPolicy) { h.builderPolicy = p }

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

func NewDeployHandler(
	apps *repositories.ApplicationRepository,
	deployments *repositories.DeploymentRepository,
	releases *repositories.ReleaseRepository,
	registries *repositories.RegistryRepository,
	gitRepos *repositories.GitRepoRepository,
	portBindings *repositories.PortBindingRepository,
	volumes *repositories.VolumeRepository,
	stackEnv *repositories.StackEnvVarRepository,
	routeRepo *repositories.RouteRepository,
	clients NodeDocker,
	bus *eventbus.Bus,
	routes RouteSyncer,
	evts events.Recorder,
	producer *Producer,
	secrets SecretResolver,
) *DeployHandler {
	return &DeployHandler{runtimeBuilder: &runtimeBuilder{stackEnv: stackEnv, routes: routeRepo, secrets: secrets}, apps: apps, deployments: deployments, releases: releases, registries: registries, gitRepos: gitRepos, portBindings: portBindings, volumes: volumes, clients: clients, bus: bus, routes: routes, events: evts, producer: producer}
}

// eng resolves the Docker client for an application's node. When the node's
// agent is disconnected it returns an offline client: critical ops fail clearly
// and best-effort cleanups (ignored results) no-op.
func (h *DeployHandler) eng(app *models.Application) docker.Client {
	dc, err := h.clients.For(app.ServerID)
	if err != nil {
		return docker.Offline(err)
	}
	return dc
}

// emit records an application event (best-effort; recorder may be nil in tests).
func (h *DeployHandler) emit(app *models.Application, dep *models.Deployment, t models.AppEventType, sev models.AppEventSeverity, message string) {
	if h.events == nil {
		return
	}
	h.events.Record(&models.AppEvent{
		WorkspaceID:   app.WorkspaceID,
		ApplicationID: app.ID,
		Type:          t,
		Severity:      sev,
		Message:       message,
		Metadata:      map[string]string{"deployment_id": fmt.Sprint(dep.ID)},
	})
}

// ProcessTask implements asynq.Handler for the deploy task.
func (h *DeployHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p DeployPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return fmt.Errorf("bad deploy payload: %w", err)
	}
	dep, err := h.deployments.FindByID(p.DeploymentID)
	if err != nil {
		return fmt.Errorf("deployment %d not found: %w", p.DeploymentID, err)
	}
	if dep.Status.IsTerminal() {
		return nil // already processed
	}
	app, err := h.apps.FindByID(dep.ApplicationID)
	if err != nil {
		return h.fail(dep, fmt.Errorf("application %d not found: %w", dep.ApplicationID, err))
	}

	// Serialize deploys per app: two concurrent deploys of the same app would
	// race on version assignment and the active-container swap. If another deploy
	// holds the lock, defer this one (re-enqueue shortly) rather than running it
	// in parallel.
	if h.deployLock != nil {
		ok, release, lerr := h.deployLock.Acquire(ctx, app.ID)
		switch {
		case lerr != nil:
			// Lock backend unavailable: prefer availability and deploy anyway.
			logger.Warn("deploy lock unavailable; proceeding without serialization", "app", app.ID, "error", lerr)
		case !ok:
			logger.Info("deploy deferred: another deploy is in progress for this app", "app", app.ID, "deployment", dep.ID)
			return h.producer.EnqueueDeployIn(dep.ID, app.ServerID, 5*time.Second)
		default:
			defer release()
		}
	}

	h.run(ctx, app, dep)
	return nil
}

func (h *DeployHandler) run(ctx context.Context, app *models.Application, dep *models.Deployment) {
	now := time.Now()
	dep.StartedAt = &now
	dep.Status = models.DeploymentBuilding
	_ = h.deployments.Update(dep)
	_ = h.apps.SetStatus(app.ID, models.AppStatusDeploying)
	h.publishStatus(dep, models.DeploymentBuilding)
	if dep.Trigger == "rollback" {
		h.emit(app, dep, models.EventRollbackStarted, models.SeverityInfo, "Rollback started")
		h.log(dep, fmt.Sprintf("rollback started — %s strategy", dep.Strategy))
	} else {
		h.emit(app, dep, models.EventDeployStarted, models.SeverityInfo, "Deployment started")
		h.log(dep, fmt.Sprintf("deployment started — %s strategy", dep.Strategy))
	}

	var image string
	// buildMethod is the resolved git build method (set by the git case below);
	// it stays empty for image/prebuilt apps. Buildpack images run via the CNB
	// launcher, which changes how the runtime spec is assembled (no custom
	// command, an injected $PORT).
	var buildMethod models.AppBuildMethod
	switch {
	case dep.ImageID != nil && dep.Image != "" && h.imagePresent(ctx, app, dep.Image):
		// Prebuilt by a pipeline run on this node: run the exact artifact
		// directly — no rebuild, no pull. This is what makes a pipeline-produced
		// deploy reproduce the precise commit+digest the run captured.
		image = dep.Image
		h.log(dep, "using prebuilt image "+image+" (no rebuild, no pull)")
	case dep.Image != "" && h.distributor != nil && h.distributor.IsBuildRef(dep.Image):
		// A registry-distributed build (e.g. a rollback to a recorded ref): run it
		// directly, pulling from the internal registry when this node lacks it —
		// this is what lets a Git-built app deploy/roll back on another node.
		image = dep.Image
		if h.imagePresent(ctx, app, image) {
			h.log(dep, "using distributed image "+image)
		} else {
			h.log(dep, "pulling distributed image "+image)
			if err := h.eng(app).PullImage(ctx, image, h.distributor.PushAuth()); err != nil {
				_ = h.fail(dep, fmt.Errorf("pull distributed image: %w", err))
				return
			}
			h.log(dep, "image pulled from internal registry")
		}
	case app.SourceType == models.AppSourceGit:
		// The image is built on a registered runner (which pushes it to the internal
		// registry); this node only pulls it — never builds. buildMethod stays the
		// app's configured method; "auto" is resolved by the runner and handled
		// safely in the runtime spec below (honor Command, inject $PORT).
		buildMethod = app.BuildMethod
		ref, err := h.buildOnRunner(ctx, app, dep)
		if err != nil {
			if errors.Is(err, runnersvc.ErrNoRunner) || errors.Is(err, runners.ErrRunnerOffline) {
				reason := ""
				if h.buildDispatch != nil {
					reason = h.buildDispatch.RunnerWaitReason(app.WorkspaceID)
				}
				h.deferForRunner(dep, app.ServerID, reason) // wait for a runner (bounded); re-enqueue
				return
			}
			if errors.Is(err, errBuildDispatchUnavailable) {
				// This worker doesn't hold the runner tunnels; hand the build off to
				// the control-plane worker that does (QueueNode) instead of failing.
				h.log(dep, "handing build off to the runner-capable worker")
				if e := h.producer.EnqueueDeployToBuilder(dep.ID); e != nil {
					_ = h.fail(dep, fmt.Errorf("hand off build to runner-capable worker: %w", e))
				}
				return
			}
			_ = h.fail(dep, err)
			return
		}
		image = ref
		dep.Image = ref
		_ = h.deployments.Update(dep)
		// Pull the runner-built image onto the target node (it lives only in the
		// registry until now).
		if h.imagePresent(ctx, app, image) {
			h.log(dep, "runner-built image "+image+" already present")
		} else {
			h.log(dep, "pulling runner-built image "+image)
			if err := h.eng(app).PullImage(ctx, image, h.distributor.PushAuth()); err != nil {
				_ = h.fail(dep, fmt.Errorf("pull built image: %w", err))
				return
			}
		}
	default:
		image = dep.Image
		if image == "" {
			image = app.ImageRef("")
		}
		dep.Image = image
		auth, err := h.resolveRegistryAuth(app, dep)
		if err != nil {
			_ = h.fail(dep, err)
			return
		}
		if auth != nil {
			h.log(dep, "authenticating to registry "+auth.Server)
		}
		// Decide whether to pull, honoring the app's image pull policy. A
		// digest-pinned ref is immutable, so an already-present one is never
		// re-pulled regardless of policy.
		present := h.imagePresent(ctx, app, image)
		pinned := strings.Contains(image, "@")
		switch {
		case present && (pinned || app.ImagePullPolicy == models.PullIfNotPresent):
			h.log(dep, "image "+image+" already present — skipping pull")
		case app.ImagePullPolicy == models.PullNever:
			if !present {
				_ = h.fail(dep, fmt.Errorf("image %s is not present locally and pull policy is 'never'", image))
				return
			}
			h.log(dep, "image "+image+" present — skipping pull (policy: never)")
		default: // always, or if-not-present with the image absent
			h.log(dep, "pulling image "+image)
			if err := h.eng(app).PullImage(ctx, image, auth); err != nil {
				_ = h.fail(dep, fmt.Errorf("pull image: %w", err))
				return
			}
			h.log(dep, "image pulled")
		}
	}

	// GPU pre-flight: capability + quota + runtime-incompatibility gates. Device
	// binding happens in the container path once the node's runtime is known.
	// Refuse GPU + cluster here (a GPU app must be single-container); the restricted
	// profile conflict is checked in the container path where the profile resolves.
	if app.GPUCount > 0 {
		if app.RuntimeKind == models.RuntimeService {
			_ = h.fail(dep, ErrGPUWithCluster)
			return
		}
		if h.gpu != nil {
			if err := h.gpu.Preflight(app); err != nil {
				_ = h.fail(dep, err)
				return
			}
		}
	}

	// Cluster apps run as a replicated Swarm service on the workspace overlay
	// (the image is already resolved above), instead of a single container.
	if app.RuntimeKind == models.RuntimeService {
		h.deployService(ctx, app, dep, image, buildMethod)
		return
	}

	h.log(dep, "creating container")
	dep.Status = models.DeploymentDeploying
	_ = h.deployments.Update(dep)
	h.publishStatus(dep, models.DeploymentDeploying)

	canary := dep.Strategy == models.DeployCanary
	// The stable release answers the app's stable alias (mb-app-<token>-<id>); a
	// canary container answers its own alias so the proxy can split weighted
	// traffic between the two.
	upstreamAlias := node.AppAlias(app)
	if canary {
		upstreamAlias = node.CanaryAlias(app)
	}
	// Container name = the alias plus the deployment id, so each deployment's
	// container is uniquely named (e.g. mb-app-5sb97dj7-27-43). Routing uses the
	// alias, not this name.
	name := fmt.Sprintf("%s-%d", node.AppAlias(app), dep.ID)
	// Assemble the shared runtime substrate (env, networks ensured on the node,
	// mounts, limits) — the same builder a one-off Job uses, so they can't drift.
	rc, err := h.buildRuntimeContext(ctx, h.eng(app), app)
	if err != nil {
		_ = h.fail(dep, fmt.Errorf("sync networks: %w", err))
		return
	}
	// Buildpack images run the CNB launcher (process type "web") as their
	// entrypoint and read $PORT — so we never override the command, and inject
	// PORT (the app's primary port, defaulting to 8080) when it isn't already set.
	cmd := app.Command
	switch buildMethod {
	case models.BuildBuildpack:
		cmd = nil
		rc.Env = ensurePortEnv(rc.Env, app.Port)
	case models.BuildAuto:
		// The runner resolved auto (Dockerfile → dockerfile, else buildpack); be
		// safe for either — honor an explicit Command and inject $PORT (required by
		// a buildpack image, harmless to a Dockerfile one).
		rc.Env = ensurePortEnv(rc.Env, app.Port)
	}
	// Deploy-specific: expose the app on its stack network by its service name
	// (slug), so sibling apps resolve it by name. A Job never registers this
	// alias (it must not impersonate the service). The alias is ignored for any
	// network the container did not actually join.
	aliasesByNet := map[string][]string{}
	if app.Stack != nil && app.Stack.DockerNetwork != "" {
		aliasesByNet[app.Stack.DockerNetwork] = []string{app.Name}
	}
	// Publish admin-approved host port bindings. A canary shares the host with
	// the stable container, so it must not re-publish the same host ports (that
	// would conflict); the canary is reachable over the proxy weight only.
	ports := map[string]string{}
	bindIPs := map[string]string{}
	if h.portBindings != nil && !canary {
		if approved, err := h.portBindings.ListApprovedByApp(app.ID); err == nil {
			for _, b := range approved {
				key := fmt.Sprintf("%d/%s", b.ContainerPort, b.Protocol)
				ports[key] = fmt.Sprintf("%d", b.HostPort)
				if b.BindIP != "" {
					bindIPs[key] = b.BindIP // publish on the node's private interface
				}
			}
		}
	}
	// Recreate strategy: stop the current container before starting the new one
	// (accepts brief downtime; required when versions can't coexist).
	if dep.Strategy == models.DeployRecreate {
		if prev, err := h.releases.FindActive(app.ID); err == nil && prev.ContainerID != "" {
			h.log(dep, "recreate: stopping current release v"+fmt.Sprint(prev.Version))
			_ = h.eng(app).StopContainer(context.Background(), prev.ContainerID, 10)
			_ = h.eng(app).RemoveContainer(context.Background(), prev.ContainerID, true)
		}
	}

	// Restricted security profile: force a non-root UID and chown the app's
	// managed volumes to it so the container can still write to them.
	sec := h.containerSecurity(app)
	if sec.Restricted() {
		h.log(dep, "security profile: restricted (running as "+sec.User+")")
		if err := h.prepareRestrictedVolumes(ctx, h.eng(app), sec, image, rc.Mounts); err != nil {
			_ = h.fail(dep, fmt.Errorf("prepare volumes: %w", err))
			return
		}
	}

	// GPU device binding: resolve the app's GPU request to concrete devices on
	// its node. Device passthrough is privileged, so it cannot coexist with the
	// restricted profile's hardening contract — refuse rather than silently drop
	// either. The node's runtimes gate whether it is GPU-capable at all.
	var gpuReqs []docker.GPURequest
	if app.GPUCount > 0 && h.gpu != nil {
		if sec.Restricted() {
			_ = h.fail(dep, ErrGPUWithRestrictedProfile)
			return
		}
		info, err := h.eng(app).Info(ctx)
		if err != nil {
			_ = h.fail(dep, fmt.Errorf("inspect node runtimes: %w", err))
			return
		}
		reqs, err := h.gpu.ResolveDevices(ctx, app, info.Runtimes)
		if err != nil {
			_ = h.fail(dep, err)
			return
		}
		gpuReqs = reqs
		h.log(dep, fmt.Sprintf("attaching %d GPU(s)", app.GPUCount))
	}
	spec := docker.RunSpec{
		Name:     name,
		Image:    image,
		Hostname: upstreamAlias, // stable alias hostname (mb-app-<token>-<id>)
		Env:      rc.Env,
		Cmd:      cmd,
		Mounts:   rc.Mounts,
		// Under the restricted profile, prepareRestrictedVolumes has already seeded
		// and chowned these volumes to the non-root UID; disable copy-up so Docker
		// doesn't re-apply the image mount-dir's ownership on start and undo it.
		NoCopyVolumes:    sec.Restricted(),
		Binds:            rc.Binds,
		Networks:         rc.Networks,
		Ports:            ports,
		PortBindIPs:      bindIPs,
		NetworkAliases:   []string{upstreamAlias}, // upstream alias for the proxy (stable or canary)
		AliasesByNetwork: aliasesByNet,
		MemoryBytes:      rc.MemoryBytes,
		NanoCPUs:         rc.NanoCPUs,
		GPUs:             gpuReqs,
		RestartPolicy:    string(app.RestartPolicy),
		Healthcheck:      buildHealthcheck(app),
		Labels:           containerLabels(app, dep.ID),
	}
	sec.applyTo(&spec)
	containerID, err := h.eng(app).RunContainer(ctx, spec)
	if err != nil {
		// On a start failure (e.g. a host port already in use) the container is
		// created but not running — remove it so a conflict doesn't leave an
		// orphaned stopped container the user has to clean up by hand.
		if containerID != "" {
			_ = h.eng(app).RemoveContainer(context.Background(), containerID, true)
		}
		_ = h.fail(dep, fmt.Errorf("run container: %w", err))
		return
	}
	dep.ContainerID = containerID
	_ = h.deployments.Update(dep)
	h.log(dep, "container created "+shortID(containerID))

	if app.HealthcheckType != models.HealthcheckNone {
		h.log(dep, "waiting for container to become healthy")
	}
	if err := h.healthGate(ctx, app, containerID); err != nil {
		_ = h.eng(app).RemoveContainer(context.Background(), containerID, true)
		_ = h.fail(dep, err)
		return
	}
	if app.HealthcheckType != models.HealthcheckNone {
		h.log(dep, "container is healthy")
	}

	if canary {
		h.releaseCanary(app, dep, image, containerID)
		return
	}
	h.swapAndRelease(app, dep, image, containerID)
}

// manager returns the manager (control-plane) Docker client, where Swarm
// services are created and inspected. Offline client on failure.
func (h *DeployHandler) manager() docker.Client {
	dc, err := h.clients.For(0)
	if err != nil {
		return docker.Offline(err)
	}
	return dc
}

// serviceNetworks resolves the swarm-scoped networks a service attaches to: the
// app's workspace networks, which in cluster mode are overlays shared with the
// workspace's databases and container apps. Each is ensured on the manager
// (create-or-reuse); an overlay is swarm-scoped, so Docker materializes it on a
// worker as soon as a task lands there.
//
// A node-local bridge cannot be attached to a swarm service, and a service that
// silently comes up on the wrong network looks healthy while failing to resolve
// its own database — so a workspace still on bridges is a hard, explanatory
// failure rather than a broken deploy.
func (h *DeployHandler) serviceNetworks(ctx context.Context, mgr docker.Client, app *models.Application) ([]string, error) {
	if len(app.Networks) == 0 {
		return nil, fmt.Errorf("app %q has no workspace network to attach to", app.Name)
	}
	out := make([]string, 0, len(app.Networks))
	for i := range app.Networks {
		n := app.Networks[i]
		if n.Driver != network.DriverOverlay {
			return nil, fmt.Errorf(
				"workspace network %q is a node-local bridge, which a replicated service cannot join. "+
					"Enable cluster networking so workspace networks become overlays — otherwise this service "+
					"could not reach its databases", n.Name)
		}
		// Carve the subnet from the Miabi pool (as bridges do) so swarm's own address
		// pool can't exhaust; fall back to swarm defaults when the allocator is unset.
		spec := docker.NetworkSpec{Name: n.DockerName, Driver: network.DriverOverlay, Attachable: true, Encrypted: true, Internal: n.Internal}
		var err error
		if h.alloc != nil {
			_, _, err = h.alloc.EnsureManaged(ctx, mgr, spec, 0, models.NetAllocKindOverlay)
		} else {
			_, err = mgr.EnsureNetworkSpec(ctx, spec)
		}
		if err != nil {
			return nil, fmt.Errorf("ensure overlay network %s: %w", n.DockerName, err)
		}
		out = append(out, n.DockerName)
	}
	return out, nil
}

// deployService deploys (or updates in place) a cluster app as a replicated
// Swarm service. Swarm performs the rolling task replacement, so there is no
// previous container to retire. The service joins two overlays: its per-workspace
// overlay for encrypted east-west traffic (tenant isolation), and the shared
// ingress overlay so the central gateway can reach its VIP for public ingress.
// Route sync (Goma → service VIP) happens in releaseService.
func (h *DeployHandler) deployService(ctx context.Context, app *models.Application, dep *models.Deployment, image string, buildMethod models.AppBuildMethod) {
	mgr := h.manager()
	sw, err := mgr.Swarm(ctx)
	if err != nil || sw.LocalNodeState != "active" || !sw.ControlAvailable {
		_ = h.fail(dep, fmt.Errorf("cluster mode is not enabled; cannot deploy %q as a swarm service", app.Name))
		return
	}

	h.log(dep, "creating service")
	dep.Status = models.DeploymentDeploying
	_ = h.deployments.Update(dep)
	h.publishStatus(dep, models.DeploymentDeploying)

	// The app's workspace networks — the SAME ones the workspace's databases and
	// container apps are on, so a service resolves a database by its alias on any
	// node. A service-only network would leave it unable to see its own database.
	svcNets, nerr := h.serviceNetworks(ctx, mgr, app)
	if nerr != nil {
		_ = h.fail(dep, nerr)
		return
	}

	// Shared ingress overlay (attachable, encrypted) the central gateway joins to
	// reach this service's VIP for public ingress. A single network for the whole
	// install, so a plain create-or-reuse is enough (no per-workspace subnet).
	if _, err := mgr.CreateOverlayNetwork(ctx, node.IngressOverlay); err != nil {
		_ = h.fail(dep, fmt.Errorf("ensure ingress overlay network: %w", err))
		return
	}

	// Resolved env (reused from the shared builder); buildpack images read $PORT
	// and run the CNB launcher, so we don't override their command.
	env, err := h.buildEnv(app)
	if err != nil {
		_ = h.fail(dep, fmt.Errorf("build env: %w", err))
		return
	}
	cmd := app.Command
	switch buildMethod {
	case models.BuildBuildpack:
		cmd = nil
		env = ensurePortEnv(env, app.Port)
	case models.BuildAuto:
		// Runner-resolved auto: honor Command, inject $PORT (safe for either method).
		env = ensurePortEnv(env, app.Port)
	}
	// Managed volume mounts only — privileged host-preset binds are not supported
	// for services (tasks may land on any node). For a shared (nfs/cifs) volume,
	// carry its driver config on the mount so every node the task lands on
	// materializes the real backing share instead of an empty local volume.
	mounts := map[string]string{}
	mountDrivers := map[string]docker.ServiceMountDriver{}
	var binds []docker.ServiceBind
	for _, m := range app.Mounts {
		if m.HostPreset != "" {
			continue // preset host binds aren't supported on services
		}
		// A host-path volume (under /mnt/*) binds an operator-managed path present on
		// every node — a bind mount, not a Docker named volume.
		if m.HostPath != "" {
			binds = append(binds, docker.ServiceBind{Source: m.HostPath, Target: m.Path, ReadOnly: m.ReadOnly})
			continue
		}
		if m.DockerName != "" {
			mounts[m.DockerName] = m.Path
			if dc := h.sharedMountDriver(app.WorkspaceID, m.VolumeID); dc != nil {
				mountDrivers[m.DockerName] = *dc
			}
		}
	}

	replicas := uint64(1)
	if app.Replicas > 0 {
		replicas = uint64(app.Replicas)
	}
	// Registry credentials the swarm distributes to worker nodes so their tasks can
	// pull the image (without this, a private-registry image — including every
	// built-in-registry build — pulls fine on the manager but fails on every
	// worker). Built-in-registry builds authenticate with the platform token; an
	// image app from an external private registry uses its stored credential.
	var regAuth *docker.RegistryAuth
	if h.distributor != nil && h.distributor.IsBuildRef(image) {
		regAuth = h.distributor.PushAuth()
	} else if a, err := h.resolveRegistryAuth(app, dep); err != nil {
		h.log(dep, "WARN: could not resolve registry auth for workers: "+err.Error())
	} else {
		regAuth = a
	}
	// Stable service name = the app's alias, so redeploys update the same service
	// in place; the alias also resolves to the service VIP via Swarm embedded DNS.
	alias := node.AppAlias(app)
	sec := h.containerSecurity(app)
	spec := docker.ServiceSpec{
		Name:           alias,
		Image:          image,
		Env:            env,
		Cmd:            cmd,
		Replicas:       replicas,
		Networks:       svcNets,
		NetworkAliases: []string{alias, app.Name},
		// Also join the shared ingress overlay, but register only the globally-unique
		// upstream alias there (never app.Name, which is workspace-scoped) — that is
		// the name the central gateway resolves to front the VIP.
		IngressNetwork: node.IngressOverlay,
		IngressAlias:   alias,
		Mounts:         mounts,
		MountDrivers:   mountDrivers,
		Binds:          binds,
		MemoryBytes:    app.MemoryBytes,
		NanoCPUs:       app.NanoCPUs,
		Constraints:    app.PlacementConstraints,
		Healthcheck:    buildHealthcheck(app),
		User:           sec.User,
		Labels:         containerLabels(app, dep.ID),
		RegistryAuth:   regAuth,
	}
	if app.UpdateConfig != nil {
		if app.UpdateConfig.Parallelism > 0 {
			spec.UpdateParallelism = uint64(app.UpdateConfig.Parallelism)
		}
		if app.UpdateConfig.DelaySeconds > 0 {
			spec.UpdateDelay = time.Duration(app.UpdateConfig.DelaySeconds) * time.Second
		}
	}

	// Create or update in place (Swarm rolls the tasks).
	exists := false
	if list, lerr := mgr.ServiceList(ctx); lerr == nil {
		for _, s := range list {
			if s.Name == alias {
				exists = true
				break
			}
		}
	}
	if exists {
		h.log(dep, fmt.Sprintf("updating service %s (%d replica(s))", alias, replicas))
		if err := mgr.ServiceUpdate(ctx, alias, spec); err != nil {
			_ = h.fail(dep, fmt.Errorf("update service: %w", err))
			return
		}
	} else {
		h.log(dep, fmt.Sprintf("creating service %s (%d replica(s))", alias, replicas))
		if _, err := mgr.ServiceCreate(ctx, spec); err != nil {
			_ = h.fail(dep, fmt.Errorf("create service: %w", err))
			return
		}
	}

	h.log(dep, "waiting for service tasks to converge")
	if err := h.serviceConverge(ctx, mgr, alias, replicas); err != nil {
		_ = h.fail(dep, err)
		return
	}
	h.log(dep, "service is running")

	// Record the release keyed by the service's Docker ID. If inspect fails, fall
	// back to the stable alias rather than persisting an empty ID (which would
	// strand the service — later scale/restart/remove couldn't resolve it).
	serviceID := alias
	if st, err := mgr.ServiceInspect(ctx, alias); err == nil && st.ID != "" {
		serviceID = st.ID
	} else if err != nil {
		h.log(dep, fmt.Sprintf("warning: could not inspect service %s (%v); recording by name", alias, err))
	}
	h.releaseService(app, dep, image, serviceID)
}

// sharedMountDriver returns the Docker volume driver config for a managed volume
// when it is shared (nfs/cifs) storage, so a swarm service mount recreates the
// same backing share on every node a task lands on. Returns nil for a node-local
// volume, a missing/unreadable volume, or when the volume repo isn't wired — the
// mount then falls back to a plain named volume (correct for node-local storage).
func (h *DeployHandler) sharedMountDriver(workspaceID, volumeID uint) *docker.ServiceMountDriver {
	if h.volumes == nil || volumeID == 0 {
		return nil
	}
	v, err := h.volumes.FindInWorkspace(workspaceID, volumeID)
	if err != nil || strings.TrimSpace(v.DriverOptsEnc) == "" {
		return nil
	}
	raw, err := crypto.Decrypt(v.DriverOptsEnc)
	if err != nil {
		return nil
	}
	opts := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &opts); err != nil || len(opts) == 0 {
		return nil
	}
	// nfs/cifs volumes are backed by Docker's built-in local driver with mount
	// options (type/device/o), so the swarm mount uses the "local" driver too.
	return &docker.ServiceMountDriver{Name: models.VolumeDriverLocal, Options: opts}
}

// serviceConverge waits until the service has at least its desired number of
// running tasks (or times out).
func (h *DeployHandler) serviceConverge(ctx context.Context, mgr docker.Client, name string, desired uint64) error {
	if desired == 0 {
		desired = 1
	}
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		st, err := mgr.ServiceInspect(ctx, name)
		if err == nil && st.RunningTasks >= desired {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("service did not reach %d running task(s) in time", desired)
}

// releaseService records the now-active release for a cluster (service) app. The
// service is updated in place, so there is no previous container to retire and
// the service id is stored on the release for lifecycle ops.
func (h *DeployHandler) releaseService(app *models.Application, dep *models.Deployment, image, serviceID string) {
	version, _ := h.releases.NextVersion(app.ID)
	rel := &models.Release{
		ApplicationID: app.ID, DeploymentID: dep.ID, Version: version,
		Image: image, ContainerID: serviceID, Active: true,
		Commit: dep.Commit, ImageID: dep.ImageID,
	}
	if err := h.releases.Create(rel); err != nil {
		_ = h.fail(dep, fmt.Errorf("create release: %w", err))
		return
	}
	_ = h.releases.Activate(app.ID, rel.ID)

	finished := time.Now()
	dep.Status = models.DeploymentSucceeded
	dep.FinishedAt = &finished
	_ = h.deployments.Update(dep)
	if app.SourceType != models.AppSourceGit && image != "" {
		img, tag := models.SplitImageRef(image)
		_ = h.apps.SetCurrentReleaseImage(app.ID, rel.ID, models.AppStatusRunning, img, tag)
	} else {
		_ = h.apps.SetCurrentRelease(app.ID, rel.ID, models.AppStatusRunning)
	}

	// Re-assert the proxy route to the service VIP on every deploy, not only on
	// route CRUD: SyncRoute re-renders the route and drives the ingress reconcile
	// (central gateway → shared ingress overlay), so a redeploy restores routing
	// instead of leaving it to whatever state the last route change left behind.
	if h.routes != nil {
		if err := h.routes.SyncRoute(context.Background(), app.ID); err != nil {
			h.log(dep, "WARN: failed to sync proxy route: "+err.Error())
		} else {
			h.log(dep, "proxy routes synced")
		}
	}

	h.tagReleaseImage(app, dep, version)
	h.log(dep, fmt.Sprintf("deployment succeeded (service release v%d)", version))
	h.publishStatus(dep, models.DeploymentSucceeded)
	if h.events != nil {
		h.events.Record(&models.AppEvent{
			WorkspaceID: app.WorkspaceID, ApplicationID: app.ID,
			Type: models.EventDeploySucceeded, Severity: models.SeverityInfo,
			Message:  fmt.Sprintf("Deployed service release v%d (%s)", version, image),
			Metadata: map[string]string{"deployment_id": fmt.Sprint(dep.ID), "deployment_number": fmt.Sprint(dep.Number), "release_version": fmt.Sprint(version), "image": image, "runtime": "service"},
		})
	}
	logger.Info("service deployment succeeded", "app", app.ID, "deployment", dep.ID, "release", version)
	h.externalizeLog(dep.ID)
}

// defaultCanaryWeight is the initial share of traffic sent to a new canary.
const defaultCanaryWeight = 10

// releaseCanary records a canary release running alongside the stable one and
// points a weighted share of traffic at it, without retiring the stable release.
func (h *DeployHandler) releaseCanary(app *models.Application, dep *models.Deployment, image, containerID string) {
	// Replace any previous canary container (re-running canary supersedes it).
	if cur, err := h.apps.FindByID(app.ID); err == nil && cur.CanaryReleaseID != nil {
		if prev, err := h.releases.FindByID(*cur.CanaryReleaseID); err == nil {
			if prev.ContainerID != "" && prev.ContainerID != containerID {
				_ = h.eng(app).StopContainer(context.Background(), prev.ContainerID, 10)
				_ = h.eng(app).RemoveContainer(context.Background(), prev.ContainerID, true)
			}
			h.finalizeCanary(prev.DeploymentID, models.DeploymentSucceeded, "superseded by a newer canary")
		}
		_ = h.releases.Delete(*cur.CanaryReleaseID)
	}

	version, _ := h.releases.NextVersion(app.ID)
	rel := &models.Release{
		ApplicationID: app.ID, DeploymentID: dep.ID, Version: version,
		Image: image, ContainerID: containerID, Active: false,
		Commit: dep.Commit, ImageID: dep.ImageID,
	}
	if err := h.releases.Create(rel); err != nil {
		_ = h.fail(dep, fmt.Errorf("create canary release: %w", err))
		return
	}
	h.tagReleaseImage(app, dep, version)

	weight := canaryInitialWeight(app)
	_ = h.apps.SetCanary(app.ID, &rel.ID, weight)
	// Both stable and canary are now serving — the app is running, not "deploying".
	_ = h.apps.SetStatus(app.ID, models.AppStatusRunning)

	// The canary deployment stays non-terminal (status: canary) while the rollout
	// progresses, so its log stream keeps streaming advance/promote/abort lines
	// live until the rollout finishes (finalizeCanary).
	dep.Status = models.DeploymentCanary
	_ = h.deployments.Update(dep)

	if h.routes != nil {
		if err := h.routes.SyncRoute(context.Background(), app.ID); err != nil {
			h.log(dep, "WARN: failed to sync proxy route: "+err.Error())
		}
	}
	h.log(dep, fmt.Sprintf("canary release v%d live at %d%% traffic", version, weight))
	h.publishStatus(dep, models.DeploymentCanary)
	if h.events != nil {
		h.events.Record(&models.AppEvent{
			WorkspaceID: app.WorkspaceID, ApplicationID: app.ID,
			Type: models.EventDeploySucceeded, Severity: models.SeverityInfo,
			Message:  fmt.Sprintf("Canary release v%d live (%d%% traffic)", version, weight),
			Metadata: map[string]string{"deployment_id": fmt.Sprint(dep.ID), "deployment_number": fmt.Sprint(dep.Number), "release_version": fmt.Sprint(version), "canary": "true"},
		})
	}
	logger.Info("canary release live", "app", app.ID, "deployment", dep.ID, "release", version, "weight", weight)

	// Schedule the first auto-progression step, unless already at full traffic.
	if weight < 100 && h.producer != nil {
		_ = h.producer.EnqueueCanaryStep(dep.ID, canaryInterval(app), app.ServerID)
	}
}

// canaryInitialWeight / canaryInterval / canaryStep read the app's tuning with
// safe fallbacks (older rows may have zero values).
func canaryInitialWeight(app *models.Application) int {
	if app.CanaryInitialWeight >= 1 && app.CanaryInitialWeight <= 99 {
		return app.CanaryInitialWeight
	}
	return defaultCanaryWeight
}

func canaryStep(app *models.Application) int {
	if app.CanaryStepWeight >= 1 && app.CanaryStepWeight <= 99 {
		return app.CanaryStepWeight
	}
	return 20
}

func canaryInterval(app *models.Application) int {
	if app.CanaryStepIntervalSeconds >= 10 {
		return app.CanaryStepIntervalSeconds
	}
	return 60
}

// nextCanaryWeight returns the weight after one step, capped at 100.
func nextCanaryWeight(current, step int) int {
	n := current + step
	if n > 100 {
		return 100
	}
	return n
}

// finalizeCanary marks an in-progress canary deployment terminal (promoted,
// superseded, or aborted), logging a closing line and ending its live log
// stream. No-op when the deployment is gone or already terminal.
func (h *DeployHandler) finalizeCanary(deploymentID uint, status models.DeploymentStatus, line string) {
	if deploymentID == 0 {
		return
	}
	dep, err := h.deployments.FindByID(deploymentID)
	if err != nil || dep.Status.IsTerminal() {
		return
	}
	if line != "" {
		h.logTo(deploymentID, line)
	}
	finished := time.Now()
	dep.Status = status
	dep.FinishedAt = &finished
	_ = h.deployments.Update(dep)
	h.bus.Publish(DeployTopic(deploymentID), eventbus.Event{Type: "status", Data: string(status)})
	h.externalizeLog(deploymentID)
}

// retireCanary stops and removes an app's canary container and clears its canary
// state, so a normal deploy cleanly supersedes an in-progress canary.
func (h *DeployHandler) retireCanary(app *models.Application, dep *models.Deployment) {
	cur, err := h.apps.FindByID(app.ID)
	if err != nil || cur.CanaryReleaseID == nil {
		return
	}
	if rel, err := h.releases.FindByID(*cur.CanaryReleaseID); err == nil {
		h.log(dep, "retiring canary release v"+fmt.Sprint(rel.Version))
		if rel.ContainerID != "" {
			_ = h.eng(app).StopContainer(context.Background(), rel.ContainerID, 10)
			_ = h.eng(app).RemoveContainer(context.Background(), rel.ContainerID, true)
		}
		h.finalizeCanary(rel.DeploymentID, models.DeploymentSucceeded, fmt.Sprintf("canary superseded by deployment #%d", dep.Number))
		_ = h.releases.Delete(rel.ID)
	}
	_ = h.apps.SetCanary(app.ID, nil, 0)
}

// ProcessCanaryStep advances an in-progress canary one step: it health-checks
// the canary container (auto-aborting on failure), then either bumps the traffic
// weight and reschedules, or auto-promotes once the weight reaches 100%.
func (h *DeployHandler) ProcessCanaryStep(ctx context.Context, task *asynq.Task) error {
	var p CanaryStepPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return nil // unprocessable payload; drop
	}
	app, err := h.apps.FindByID(canaryAppID(h, p.DeploymentID))
	if err != nil || app.CanaryReleaseID == nil {
		return nil // canary already promoted/aborted
	}
	rel, err := h.releases.FindByID(*app.CanaryReleaseID)
	if err != nil {
		return nil
	}
	// Stale guard: a newer canary deploy superseded this rollout loop.
	if rel.DeploymentID != p.DeploymentID {
		return nil
	}

	// Health gate: if the canary container died, roll back automatically.
	if rel.ContainerID != "" {
		if c, err := h.eng(app).InspectContainer(ctx, rel.ContainerID); err == nil {
			if c.State == "exited" || c.State == "dead" {
				h.autoAbortCanary(app, rel)
				return nil
			}
		}
	}

	next := nextCanaryWeight(app.CanaryWeight, canaryStep(app))
	if next >= 100 {
		h.autoPromoteCanary(app, rel)
		return nil
	}
	_ = h.apps.SetCanary(app.ID, app.CanaryReleaseID, next)
	if h.routes != nil {
		_ = h.routes.SyncRoute(context.Background(), app.ID)
	}
	h.recordCanary(app, rel.DeploymentID, models.SeverityInfo, fmt.Sprintf("Canary advanced to %d%% traffic", next))
	if h.producer != nil {
		_ = h.producer.EnqueueCanaryStep(p.DeploymentID, canaryInterval(app), app.ServerID)
	}
	logger.Info("canary advanced", "app", app.ID, "weight", next)
	return nil
}

// canaryAppID resolves the application id for a canary deployment id (helper so
// ProcessCanaryStep stays linear). Returns 0 when the deployment is gone.
func canaryAppID(h *DeployHandler, deploymentID uint) uint {
	dep, err := h.deployments.FindByID(deploymentID)
	if err != nil {
		return 0
	}
	return dep.ApplicationID
}

// autoPromoteCanary enqueues a normal rolling deploy of the canary's image; the
// deploy pipeline retires both the old stable and the canary container.
func (h *DeployHandler) autoPromoteCanary(app *models.Application, rel *models.Release) {
	image := ""
	if app.SourceType != models.AppSourceGit {
		image = rel.Image
	}
	dep := &models.Deployment{
		ApplicationID: app.ID, Image: image, Trigger: "auto",
		Strategy: models.DeployRolling, RegistryID: app.RegistryID, Status: models.DeploymentPending,
	}
	if err := h.deployments.Create(dep); err != nil {
		logger.Error("canary auto-promote: create deployment", "app", app.ID, "error", err)
		return
	}
	if h.producer != nil {
		_ = h.producer.EnqueueDeploy(dep.ID, app.ServerID)
	}
	h.recordCanary(app, rel.DeploymentID, models.SeverityInfo, "Canary reached 100% — auto-promoting to stable")
	logger.Info("canary auto-promote", "app", app.ID, "release", rel.Version)
}

// autoAbortCanary stops the canary container and returns all traffic to stable.
func (h *DeployHandler) autoAbortCanary(app *models.Application, rel *models.Release) {
	if rel.ContainerID != "" {
		_ = h.eng(app).StopContainer(context.Background(), rel.ContainerID, 10)
		_ = h.eng(app).RemoveContainer(context.Background(), rel.ContainerID, true)
	}
	_ = h.releases.Delete(rel.ID)
	_ = h.apps.SetCanary(app.ID, nil, 0)
	_ = h.apps.SetStatus(app.ID, models.AppStatusRunning)
	if h.routes != nil {
		_ = h.routes.SyncRoute(context.Background(), app.ID)
	}
	h.recordCanary(app, rel.DeploymentID, models.SeverityWarning, "Canary container unhealthy — auto-aborted, all traffic on stable")
	h.finalizeCanary(rel.DeploymentID, models.DeploymentFailed, "")
	logger.Warn("canary auto-abort", "app", app.ID, "release", rel.Version)
}

// recordCanary writes a canary-progression message to BOTH the originating
// deployment's log (deploymentID) and the app event stream, so canary advances,
// auto-promotion, and auto-abort all show up in Deployment Logs.
func (h *DeployHandler) recordCanary(app *models.Application, deploymentID uint, sev models.AppEventSeverity, message string) {
	if deploymentID != 0 {
		h.logTo(deploymentID, message)
	}
	if h.events == nil {
		return
	}
	t := models.EventDeploySucceeded
	if sev == models.SeverityWarning {
		t = models.EventDeployFailed
	}
	h.events.Record(&models.AppEvent{
		WorkspaceID: app.WorkspaceID, ApplicationID: app.ID,
		Type: t, Severity: sev, Message: message,
		Metadata: map[string]string{"canary": "true"},
	})
}

// containerLabels composes the full Docker label set for an app's container or
// service: the user's sanitized custom labels first (Traefik &c.), then the
// platform's own system labels — app + deployment id, and the workspace / stack /
// compose keys via stackLabels — stamped on top so a system label always wins a
// key collision. SanitizeUserLabels has already dropped any reserved keys, so
// this is belt-and-suspenders.
func containerLabels(app *models.Application, deploymentID uint) map[string]string {
	labels := docker.SanitizeUserLabels(app.ContainerLabels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[docker.LabelApp] = fmt.Sprintf("%d", app.ID)
	labels[docker.LabelDeployment] = fmt.Sprintf("%d", deploymentID)
	return stackLabels(app, labels)
}

// stackLabels stamps the owning workspace on every app container/service (so the
// node-container view can be scoped per workspace) and, when the app belongs to a
// stack, adds Docker Compose project labels so native Docker tooling
// (`docker compose ls`, Docker Desktop) groups the container under the stack
// alongside Miabi's own grouping key.
func stackLabels(app *models.Application, base map[string]string) map[string]string {
	base[docker.LabelWorkspace] = fmt.Sprintf("%d", app.WorkspaceID)
	if app.Stack == nil {
		return base
	}
	base["com.docker.compose.project"] = app.Stack.DockerName
	base["com.docker.compose.service"] = app.Name
	base[docker.LabelStack] = fmt.Sprintf("%d", app.Stack.ID)
	return base
}

// imagePresent reports whether ref is already on the app's node. Errors (e.g. a
// disconnected node) report absent so the caller falls back to building/pulling.
func (h *DeployHandler) imagePresent(ctx context.Context, app *models.Application, ref string) bool {
	ok, err := h.eng(app).ImageExists(ctx, ref)
	return err == nil && ok
}

// ErrRegistryRequired is returned when a git-source app deploys but no image
// distributor is wired at all: the runner builds the image and pushes it to the
// internal registry for the target node to pull, so the registry is a hard
// dependency for git builds. When a distributor IS wired but misconfigured, the
// deploy surfaces the specific reason from DistributionUnavailableReason instead.
var ErrRegistryRequired = errors.New("git-source deploys require the internal registry (set MIABI_REGISTRY_ENABLED): the runner builds the image and pushes it there for the node to pull")

// errBuildDispatchUnavailable is an internal sentinel: this worker has no runner
// build dispatch wired (it doesn't hold the runner tunnels), so the git-source
// deploy is handed off to the control-plane worker that does, rather than failed.
var errBuildDispatchUnavailable = errors.New("runner build dispatch is not configured on this worker")

// deployRunnerWaitInterval is how long a deploy waits before re-checking for a
// free runner while it queues.
const deployRunnerWaitInterval = 15 * time.Second

// buildOnRunner dispatches a git-source app's image build to a registered runner
// (which clones the source, builds per the app's config, and pushes to the
// internal registry) and returns the pushed digest ref for the node to pull. The
// build never runs on this node. Returns runnersvc.ErrNoRunner /
// runners.ErrRunnerOffline unchanged so the caller can queue the deploy.
func (h *DeployHandler) buildOnRunner(ctx context.Context, app *models.Application, dep *models.Deployment) (string, error) {
	if h.distributor == nil {
		return "", ErrRegistryRequired
	}
	if reason := h.distributor.DistributionUnavailableReason(); reason != "" {
		return "", fmt.Errorf("git-source deploys require the internal registry: %s", reason)
	}
	if h.buildDispatch == nil {
		return "", errBuildDispatchUnavailable
	}
	buildRef := h.distributor.BuildRef(app.WorkspaceID, app.Name, dep.ID)
	repository, _ := models.SplitImageRef(buildRef)
	if repository == "" {
		return "", fmt.Errorf("could not resolve a registry repository for the build")
	}
	// The runner logs into Registry and pushes to Repository, so the two hosts MUST
	// match. Repository carries the registry's *resolved* host (BuildRef →
	// HostFor: explicit host, else registry.<base-domain>); h.registryHost is only
	// the raw MIABI_REGISTRY_HOST env, which is empty when the host is derived or
	// set via the UI. Using it as the login host would make the runner log into a
	// different (or empty) host than it pushes to → "denied". Derive the login host
	// from Repository so they are the same by construction.
	registryHost := repository
	if i := strings.IndexByte(repository, '/'); i >= 0 {
		registryHost = repository[:i]
	}
	sourceURL, err := h.gitSourceURL(app)
	if err != nil {
		return "", err
	}
	h.log(dep, "dispatching build to a runner")
	digest, err := h.buildDispatch.RunBuild(ctx, runners.BuildInputs{
		DeploymentID:     dep.ID,
		DeploymentNumber: dep.Number,
		WorkspaceID:      app.WorkspaceID,
		AppID:            app.ID,
		AppName:          app.Name,
		SourceURL:        sourceURL,
		Commit:           dep.Commit,
		Ref:              app.GitRef,
		Repository:       repository,
		Registry:         registryHost,
		Build:            h.buildConfigFromApp(app),
	}, 0, func(line string) { h.log(dep, line) })
	if err != nil {
		return "", err
	}
	imageRef := repository + "@" + digest
	h.recordBuiltImage(app, dep, repository, BuildResult{Digest: digest, Runner: "runner"})
	return imageRef, nil
}

// buildConfigFromApp maps an app's build settings onto the runner build config.
// The runner resolves "auto" (Dockerfile → dockerfile, else buildpack). The
// builder image follows the platform's admin-controlled policy: the app's
// override if set, else the resolved platform default (so an admin's builder
// choice applies) — the runner only falls back to its own default when both are
// empty.
func (h *DeployHandler) buildConfigFromApp(app *models.Application) *proto.BuildConfig {
	builder := app.Builder
	// Defense-in-depth: drop a custom builder the workspace is no longer entitled
	// to (e.g. set under a plan that has since been downgraded), falling back to
	// the platform default below.
	if builder != "" && h.builderPolicy != nil && !h.builderPolicy.CustomBuilderAllowed(app.WorkspaceID) {
		builder = ""
	}
	if builder == "" && h.build.resolver != nil {
		builder = h.build.resolver.Ref(platformimage.KeyBuildpackBuilder)
	}
	return &proto.BuildConfig{
		Method:     string(app.BuildMethod),
		Builder:    builder,
		Buildpacks: app.Buildpacks,
		BuildEnv:   app.BuildEnv,
	}
}

// gitSourceURL resolves an app's git clone URL for the runner, embedding the
// linked HTTPS credential so a private repo clones on the runner (which has no
// local git auth). The URL is the app's explicit URL, else the credential's URL;
// SSH-key credentials aren't supported for runner builds (ErrSSHUnsupportedOnRunner).
func (h *DeployHandler) gitSourceURL(app *models.Application) (string, error) {
	rawURL := app.GitRepo
	var gr *models.GitRepository
	if app.GitRepositoryID != nil {
		g, err := h.gitRepos.FindInWorkspace(app.WorkspaceID, *app.GitRepositoryID)
		if err != nil {
			return "", fmt.Errorf("git credential %d: %w", *app.GitRepositoryID, err)
		}
		gr = g
		if rawURL == "" {
			rawURL = g.URL
		}
	}
	if rawURL == "" {
		return "", fmt.Errorf("git source requires a repository URL")
	}
	return gitrepo.CredentialURL(rawURL, gr)
}

// deferForRunner queues a deploy that has no available runner: it parks the
// deployment pending and re-enqueues it shortly, failing once it has waited
// longer than runnerWaitTimeout — so a git build never runs on a node, and a run
// with no runner ever registered doesn't wait forever.
func (h *DeployHandler) deferForRunner(dep *models.Deployment, serverID uint, reason string) {
	// A newer deployment for this app supersedes an older one still waiting for a
	// runner: stop looping it (up to the wait timeout) so redeploys don't pile up
	// pending — only the latest should be trying to acquire the runner.
	if latest, err := h.deployments.LatestNumberByApp(dep.ApplicationID); err == nil && latest > dep.Number {
		_ = h.fail(dep, fmt.Errorf("superseded by a newer deployment (#%d) while waiting for a runner", latest))
		return
	}
	if h.runnerWaitTimeout > 0 && time.Since(dep.CreatedAt) > h.runnerWaitTimeout {
		_ = h.fail(dep, fmt.Errorf("no runner became available within %s — register a runner (Settings → Runners)", h.runnerWaitTimeout))
		return
	}
	dep.Status = models.DeploymentPending
	dep.StartedAt = nil
	_ = h.deployments.Update(dep)
	h.publishStatus(dep, models.DeploymentPending)
	msg := "waiting for an available runner…"
	if reason != "" {
		msg += " (" + reason + ")"
	}
	h.log(dep, msg)
	if err := h.producer.EnqueueDeployIn(dep.ID, serverID, deployRunnerWaitInterval); err != nil {
		logger.Warn("re-enqueue deploy for runner failed", "deployment", dep.ID, "error", err)
	}
}

// recordBuiltImage writes provenance for a freshly built git image (best-effort:
// a recording failure must not fail an otherwise-successful deploy). No-op when
// the image catalog is unwired or the build reported no digest.
// tagReleaseImage best-effort adds a v<version> registry tag to a runner-built
// image so the registry mirrors the release number the UI shows, alongside the
// immutable build tag. No-op for external images (only internal build refs carry
// a digest we pushed) or when distribution is off.
func (h *DeployHandler) tagReleaseImage(app *models.Application, dep *models.Deployment, version int) {
	if h.distributor == nil || dep.Image == "" || !h.distributor.IsBuildRef(dep.Image) {
		return
	}
	_, digest, ok := strings.Cut(dep.Image, "@")
	if !ok || digest == "" {
		return
	}
	if err := h.distributor.TagReleaseVersion(context.Background(), app.WorkspaceID, app.Name, digest, version); err != nil {
		logger.Warn("failed to add release-version tag to image", "app", app.ID, "deployment", dep.ID, "version", version, "error", err)
	}
}

func (h *DeployHandler) recordBuiltImage(app *models.Application, dep *models.Deployment, tag string, res BuildResult) {
	if h.build.images == nil || res.Digest == "" {
		return
	}
	repo, _ := models.SplitImageRef(tag)
	if _, err := h.build.images.Record(imagesvc.RecordInput{
		WorkspaceID:   app.WorkspaceID,
		Repository:    repo,
		Tag:           fmt.Sprint(dep.ID),
		Digest:        res.Digest,
		SizeBytes:     res.Size,
		ApplicationID: &app.ID,
		Commit:        dep.Commit,
		Runner:        res.Runner,
	}); err != nil {
		logger.Warn("failed to record built image provenance", "app", app.ID, "deployment", dep.ID, "error", err)
	}
}

// ensurePortEnv sets PORT in env when not already present, so a buildpack app's
// process knows where to listen. Defaults to the app's primary port, falling
// back to 8080 when unset.
func ensurePortEnv(env []string, port int) []string {
	for _, e := range env {
		if strings.HasPrefix(e, "PORT=") {
			return env
		}
	}
	if port == 0 {
		port = 8080
	}
	return append(env, fmt.Sprintf("PORT=%d", port))
}

// resolveRegistryAuth returns the registry credential for this deploy, if any.
// A per-deploy RegistryID takes precedence over the app's default; both nil
// means an anonymous (public) pull.
func (h *DeployHandler) resolveRegistryAuth(app *models.Application, dep *models.Deployment) (*docker.RegistryAuth, error) {
	regID := dep.RegistryID
	if regID == nil {
		regID = app.RegistryID
	}
	if regID == nil {
		return nil, nil
	}
	reg, err := h.registries.FindInWorkspace(app.WorkspaceID, *regID)
	if err != nil {
		return nil, fmt.Errorf("registry %d: %w", *regID, err)
	}
	secret, err := crypto.Decrypt(reg.Secret)
	if err != nil {
		return nil, fmt.Errorf("decrypt registry secret: %w", err)
	}
	return &docker.RegistryAuth{Server: reg.Server, Username: reg.Username, Password: secret}, nil
}

// healthGate waits for the new container to be ready. With no healthcheck it
// waits for the container to reach "running"; with a healthcheck configured it
// additionally waits for Docker to report it "healthy", failing fast on an
// "unhealthy" verdict or an early exit.
func (h *DeployHandler) healthGate(ctx context.Context, app *models.Application, containerID string) error {
	requireHealthy := app.HealthcheckType == models.HealthcheckHTTP || app.HealthcheckType == models.HealthcheckCommand
	deadline := time.Now().Add(healthGateTimeout(app, requireHealthy))
	for time.Now().Before(deadline) {
		c, err := h.eng(app).InspectContainer(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspect: %w", err)
		}
		switch c.State {
		case "exited", "dead":
			return fmt.Errorf("container exited during startup (state=%s)", c.State)
		case "running":
			if !requireHealthy {
				return nil
			}
			switch c.Health {
			case "healthy":
				return nil
			case "unhealthy":
				return fmt.Errorf("container reported unhealthy during startup")
			}
			// "starting" or "" (not yet reported) — keep waiting.
		}
		time.Sleep(time.Second)
	}
	if requireHealthy {
		return fmt.Errorf("container did not become healthy in time")
	}
	return fmt.Errorf("container did not start in time")
}

// healthGateTimeout bounds the readiness wait: a short window without a
// healthcheck, or one derived from the probe's own timing (so slow-starting apps
// aren't cut off), capped to keep deploys from hanging.
func healthGateTimeout(app *models.Application, requireHealthy bool) time.Duration {
	if !requireHealthy {
		return 15 * time.Second
	}
	d := time.Duration(clampInt(app.HealthcheckStartPeriodSeconds, 0, 600))*time.Second +
		time.Duration(hcInterval(app))*time.Second*time.Duration(hcRetries(app)+1) +
		15*time.Second
	if d > 180*time.Second {
		return 180 * time.Second
	}
	return d
}

// buildHealthcheck turns an app's healthcheck config into a docker spec, or nil
// when disabled.
func buildHealthcheck(app *models.Application) *docker.HealthcheckSpec {
	var test []string
	switch app.HealthcheckType {
	case models.HealthcheckHTTP:
		port := app.HealthcheckPort
		if port == 0 {
			port = app.Port
		}
		if port == 0 {
			port = 80
		}
		path := app.HealthcheckHTTPPath
		if path == "" {
			path = "/"
		}
		url := fmt.Sprintf("http://localhost:%d%s", port, path)
		// curl when present, else wget (busybox) — covers most images.
		test = []string{"CMD-SHELL", fmt.Sprintf("curl -fsS %s || wget -qO- %s || exit 1", url, url)}
	case models.HealthcheckCommand:
		cmd := strings.TrimSpace(app.HealthcheckCommand)
		if cmd == "" {
			return nil
		}
		test = []string{"CMD-SHELL", cmd}
	default:
		return nil
	}
	return &docker.HealthcheckSpec{
		Test:        test,
		Interval:    time.Duration(hcInterval(app)) * time.Second,
		Timeout:     time.Duration(hcTimeout(app)) * time.Second,
		Retries:     hcRetries(app),
		StartPeriod: time.Duration(clampInt(app.HealthcheckStartPeriodSeconds, 0, 600)) * time.Second,
	}
}

func hcInterval(app *models.Application) int {
	return clampInt(app.HealthcheckIntervalSeconds, 1, 3600)
}
func hcTimeout(app *models.Application) int { return clampInt(app.HealthcheckTimeoutSeconds, 1, 3600) }
func hcRetries(app *models.Application) int { return clampInt(app.HealthcheckRetries, 1, 20) }

// clampInt bounds v to [lo, hi], returning a default-ish lo when v is zero.
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// swapAndRelease promotes the new container, retires the previous one, and
// records an active release.
func (h *DeployHandler) swapAndRelease(app *models.Application, dep *models.Deployment, image, containerID string) {
	// A normal deploy supersedes any in-progress canary: retire its container and
	// clear the split so the route points solely at the new stable release.
	h.retireCanary(app, dep)

	// Retire the previous active release's container (best-effort).
	if prev, err := h.releases.FindActive(app.ID); err == nil && prev.ContainerID != "" {
		h.log(dep, "retiring previous release v"+fmt.Sprint(prev.Version))
		_ = h.eng(app).StopContainer(context.Background(), prev.ContainerID, 10)
		_ = h.eng(app).RemoveContainer(context.Background(), prev.ContainerID, true)
	}

	version, _ := h.releases.NextVersion(app.ID)
	rel := &models.Release{
		ApplicationID: app.ID, DeploymentID: dep.ID, Version: version,
		Image: image, ContainerID: containerID, Active: true,
		// Provenance: a pipeline-built deploy carries its commit + catalog image so
		// the release is a reproducible artifact and GC never collects a digest the
		// active release references.
		Commit: dep.Commit, ImageID: dep.ImageID,
	}
	if err := h.releases.Create(rel); err != nil {
		_ = h.fail(dep, fmt.Errorf("create release: %w", err))
		return
	}
	_ = h.releases.Activate(app.ID, rel.ID)

	finished := time.Now()
	dep.Status = models.DeploymentSucceeded
	dep.FinishedAt = &finished
	_ = h.deployments.Update(dep)
	// Persist the deployed image/tag for image apps so the app's stored Image/Tag
	// reflect the active version and future redeploys use it (git apps build their
	// own image and keep their Git source unchanged).
	if app.SourceType != models.AppSourceGit && image != "" {
		img, tag := models.SplitImageRef(image)
		_ = h.apps.SetCurrentReleaseImage(app.ID, rel.ID, models.AppStatusRunning, img, tag)
	} else {
		_ = h.apps.SetCurrentRelease(app.ID, rel.ID, models.AppStatusRunning)
	}

	// Reconcile the reverse-proxy route to the now-active release.
	if h.routes != nil {
		if err := h.routes.SyncRoute(context.Background(), app.ID); err != nil {
			h.log(dep, "WARN: failed to sync proxy route: "+err.Error())
		} else {
			h.log(dep, "proxy routes synced")
		}
	}

	h.tagReleaseImage(app, dep, version)
	h.log(dep, fmt.Sprintf("deployment succeeded (release v%d)", version))
	h.publishStatus(dep, models.DeploymentSucceeded)
	if h.events != nil {
		h.events.Record(&models.AppEvent{
			WorkspaceID: app.WorkspaceID, ApplicationID: app.ID,
			Type: models.EventDeploySucceeded, Severity: models.SeverityInfo,
			Message:  fmt.Sprintf("Deployed release v%d (%s)", version, image),
			Metadata: map[string]string{"deployment_id": fmt.Sprint(dep.ID), "deployment_number": fmt.Sprint(dep.Number), "release_version": fmt.Sprint(version), "image": image},
		})
	}
	logger.Info("deployment succeeded", "app", app.ID, "deployment", dep.ID, "release", version)
	h.externalizeLog(dep.ID)
}

// failedAppStatus decides an application's status after a failed deploy. A
// rolling/canary deploy starts the new container alongside the old and discards
// only the new one on failure, so the previous release keeps serving — the app is
// still running. A recreate stopped the old container first, and a first-ever
// deploy (no current release) has nothing to fall back to, so those are failed.
func failedAppStatus(hasCurrentRelease bool, strategy models.DeployStrategy) models.AppStatus {
	if hasCurrentRelease && strategy != models.DeployRecreate {
		return models.AppStatusRunning
	}
	return models.AppStatusFailed
}

func (h *DeployHandler) fail(dep *models.Deployment, cause error) error {
	finished := time.Now()
	dep.Status = models.DeploymentFailed
	dep.Error = cause.Error()
	dep.FinishedAt = &finished
	_ = h.deployments.Update(dep)

	// A failed deploy must not mark the whole app failed when the previous release
	// is still serving. Rolling/canary start the new container alongside the old
	// and only discard the new one on failure, so the old release keeps running —
	// the app is still "running", just on its prior version. Only a recreate (which
	// stopped the old container first) or a first-ever deploy (no prior release)
	// leaves nothing running, so those are genuinely failed. The live container
	// health corrects any edge case on the app detail view.
	app, _ := h.apps.FindByID(dep.ApplicationID)
	hasCurrentRelease := app != nil && app.CurrentReleaseID != nil
	_ = h.apps.SetStatus(dep.ApplicationID, failedAppStatus(hasCurrentRelease, dep.Strategy))

	h.log(dep, "ERROR: "+cause.Error())
	h.publishStatus(dep, models.DeploymentFailed)
	if h.events != nil {
		wsID := uint(0)
		if app != nil {
			wsID = app.WorkspaceID
		}
		h.events.Record(&models.AppEvent{
			WorkspaceID: wsID, ApplicationID: dep.ApplicationID,
			Type: models.EventDeployFailed, Severity: models.SeverityError,
			Message:  "Deployment failed: " + cause.Error(),
			Metadata: map[string]string{"deployment_id": fmt.Sprint(dep.ID), "deployment_number": fmt.Sprint(dep.Number)},
		})
	}
	logger.Error("deployment failed", "deployment", dep.ID, "error", cause)
	h.externalizeLog(dep.ID)
	return nil // do not retry: state is recorded; a new deploy is the retry path
}

// externalizeLog moves a terminal deployment's full log from the DB column into
// the shared log store, then trims the row to a bounded tail + a reference.
// Called as the last step of every terminal transition (success, failure,
// canary finalize). No-op when the store is disabled or already externalized;
// the full log stays in the DB tail as a fallback on any store error.
func (h *DeployHandler) externalizeLog(deploymentID uint) {
	if !h.logs.Enabled() {
		return
	}
	dep, err := h.deployments.FindByID(deploymentID)
	if err != nil || dep.LogRef != "" {
		return
	}
	app, err := h.apps.FindByID(dep.ApplicationID)
	if err != nil {
		return
	}
	ref := logstore.DeploymentRef(app.WorkspaceID, app.ID, dep.ID)
	res, err := h.logs.Externalize(ref, dep.Logs)
	if err != nil {
		logger.Error("log store: externalize deployment log failed", "deployment", deploymentID, "error", err)
		return
	}
	if err := h.deployments.SetLogMeta(deploymentID, res.Ref, res.Tail, res.Bytes, res.Lines, res.Truncated); err != nil {
		logger.Error("log store: record deployment log ref failed", "deployment", deploymentID, "error", err)
	}
}

func (h *DeployHandler) log(dep *models.Deployment, line string) {
	h.logTo(dep.ID, line)
}

// shortID trims a Docker container id to its 12-char short form for logs.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// logTo appends a line to a deployment's log by id and streams it live. Used by
// the canary progression tasks, which act on a release and only know the
// originating deployment id (not the handler's dep pointer).
func (h *DeployHandler) logTo(deploymentID uint, line string) {
	_ = h.deployments.AppendLog(deploymentID, line)
	h.bus.Publish(DeployTopic(deploymentID), eventbus.Event{Type: "log", Data: line})
}

func (h *DeployHandler) publishStatus(dep *models.Deployment, status models.DeploymentStatus) {
	h.bus.Publish(DeployTopic(dep.ID), eventbus.Event{Type: "status", Data: string(status)})
}
