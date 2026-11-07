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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/datavolume"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/healthprobe"
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
	"github.com/miabi-io/miabi/internal/services/secpolicy"
	storagesvc "github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/runner/proto"
)

// DeployTopic is the eventbus topic carrying a deployment's live events.
func DeployTopic(deploymentID uint) string {
	return fmt.Sprintf("deploy:%d", deploymentID)
}

// Injected to avoid a worker->domain import.
type RouteSyncer interface {
	SyncRoute(ctx context.Context, appID uint) error
}

type DeployHandler struct {
	*runtimeBuilder
	apps          *repositories.ApplicationRepository
	deployments   *repositories.DeploymentRepository
	releases      *repositories.ReleaseRepository
	registries    *repositories.RegistryRepository
	gitRepos      *repositories.GitRepoRepository
	portBindings  *repositories.PortBindingRepository
	portPolicy    PortPublishPolicy
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
	releaseHook   ReleaseHook
	builderPolicy BuilderPolicy
	poolPolicy    PoolPolicy
	clusterSlots  *clusterSlots

	// A git-source app's image is built on a runner and pushed to the internal registry, never on this node.
	buildDispatch     BuildDispatcher
	registryHost      string        // MIABI_REGISTRY host runners push to / login
	runnerWaitTimeout time.Duration // how long a deploy waits for a runner before failing

	// Optional; nil deploys ignore GPUs.
	gpu GPUScheduler
}

type GPUScheduler interface {
	// Preflight checks the plan capability and GPU quota without binding devices.
	Preflight(app *models.Application) error
	ResolveDevices(ctx context.Context, app *models.Application, runtimes []string) ([]docker.GPURequest, error)
}

// Without it a GPU request is ignored at deploy; the request-time capability gate still applies.
func (h *DeployHandler) SetGPU(g GPUScheduler) { h.gpu = g }

type PortPublishPolicy interface {
	CheckPortPublish(p secpolicy.PortPublish) secpolicy.PublishDecision
}

// Unset publishes every approved binding on all interfaces, as Community does.
func (h *DeployHandler) SetPortPolicy(p PortPublishPolicy) { h.portPolicy = p }

// These fail the deploy clearly rather than silently dropping the GPU or the conflicting setting.
var (
	ErrGPUWithCluster           = errors.New("GPU apps must run as a single container, not a clustered (replicated) service; set the runtime to container")
	ErrGPUWithRestrictedProfile = errors.New("GPU device passthrough is incompatible with the restricted security profile; use the default profile for GPU apps")
)

type BuildDispatcher interface {
	RunBuild(ctx context.Context, in runners.BuildInputs, subjectUserID uint, onLog func(string)) (string, error)
	RunnerWaitReason(workspaceID uint) string
}

func (h *DeployHandler) SetBuildDispatch(d BuildDispatcher, registryHost string, runnerWaitTimeout time.Duration) {
	h.buildDispatch = d
	h.registryHost = registryHost
	h.runnerWaitTimeout = runnerWaitTimeout
}

// nil runs deploys without cross-worker serialization.
func (h *DeployHandler) SetDeployLock(l DeployLock) { h.deployLock = l }

// ReleaseHook must not block the deploy.
type ReleaseHook func(appID, releaseID uint)

// SetReleaseHook wires what runs on release activation (onRelease Jobs).
func (h *DeployHandler) SetReleaseHook(fn ReleaseHook) { h.releaseHook = fn }

func (h *DeployHandler) releaseActivated(appID, releaseID uint) {
	if h.releaseHook != nil {
		h.releaseHook(appID, releaseID)
	}
}

// SetClusterConcurrency caps how many deploys run at once per cluster (0 disables the cap).
func (h *DeployHandler) SetClusterConcurrency(limit int) {
	if limit > 0 {
		h.clusterSlots = newClusterSlots(limit)
	}
}

// When set, the full log goes to the store on terminal state and the DB row keeps a bounded tail.
func (h *DeployHandler) SetLogStore(s *logstore.Store) { h.logs = s }

// Distributor pushes built images to the internal registry so any node can pull them; nil keeps builds node-local.
type Distributor interface {
	DistributionEnabled() bool
	DistributionUnavailableReason() string
	// BuildRef is the registry ref a build is distributed under: registry.<domain>/<workspace>/<app>:<deploymentID>.
	BuildRef(workspaceID uint, appName string, deploymentID uint) string
	// TagReleaseVersion best-effort tags the pushed image v<version> so the registry mirrors the release number.
	TagReleaseVersion(ctx context.Context, workspaceID uint, appName, digest string, version int) error
	// IsBuildRef answers "whose registry", never "whose image"; see ResolveImageRef.
	IsBuildRef(ref string) bool
	// ResolveImageRef requires internal-registry refs to be in the workspace's namespace, rewritten to the ws_<id> form.
	ResolveImageRef(workspaceID uint, ref string) (string, error)
	// PushAuth is the credential to push/pull distributed images.
	PushAuth() *docker.RegistryAuth
}

// SetDistributor wires the internal-registry image distributor (optional).
func (h *DeployHandler) SetDistributor(d Distributor) { h.distributor = d }

type buildConfig struct {
	resolver ImageRefResolver
	images   *imagesvc.Service
}

func (h *DeployHandler) SetBuildProvenance(resolver ImageRefResolver, images *imagesvc.Service) {
	h.build = buildConfig{resolver: resolver, images: images}
}

type BuilderPolicy interface {
	CustomBuilderAllowed(workspaceID uint) bool
}

// A builder set while granted stops being honored if the capability is later revoked; nil honors the app's builder.
func (h *DeployHandler) SetBuilderPolicy(p BuilderPolicy) { h.builderPolicy = p }

type PoolPolicy interface {
	PoolConstraints(workspaceID, clusterID uint) []string
	NodeConstraint(serverID uint) string
}

// SetPoolPolicy wires plan node pools into service placement (nil adds no pool constraints).
func (h *DeployHandler) SetPoolPolicy(p PoolPolicy) { h.poolPolicy = p }

func (h *DeployHandler) serviceConstraints(app *models.Application) []string {
	if h.poolPolicy == nil {
		return app.PlacementConstraints
	}
	out := append(append([]string{}, app.PlacementConstraints...), h.poolPolicy.PoolConstraints(app.WorkspaceID, app.ClusterID)...)
	// A node-local volume exists on one node: a task scheduled anywhere else would start on a new, empty one.
	for _, pin := range h.volumePins(app) {
		if !slices.Contains(out, pin) {
			out = append(out, pin)
		}
	}
	return out
}

func (h *DeployHandler) volumePins(app *models.Application) []string {
	if h.volumes == nil {
		return nil
	}
	var out []string
	for _, m := range app.Mounts {
		if m.VolumeID == 0 || m.HostPath != "" {
			continue
		}
		v, err := h.volumes.FindInWorkspace(app.WorkspaceID, m.VolumeID)
		if err != nil || v.Driver == models.VolumeDriverHost || v.AccessMode == models.AccessRWX {
			continue
		}
		if pin := h.poolPolicy.NodeConstraint(v.ServerID); pin != "" && !slices.Contains(out, pin) {
			out = append(out, pin)
		}
	}
	return out
}

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

// A disconnected node yields an offline client: critical ops fail clearly and best-effort cleanups no-op.
func (h *DeployHandler) eng(app *models.Application) docker.Client {
	dc, err := h.clients.For(app.ServerID)
	if err != nil {
		return docker.Offline(err)
	}
	return dc
}

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

	if h.clusterSlots != nil {
		release, ok := h.clusterSlots.acquire(app.ClusterID)
		if !ok {
			logger.Info("deploy deferred: its cluster is at its deploy concurrency", "app", app.ID, "cluster", app.ClusterID, "deployment", dep.ID)
			return h.producer.EnqueueDeployIn(dep.ID, app.ServerID, 5*time.Second)
		}
		defer release()
	}

	// Concurrent deploys of one app would race on version assignment and the container swap, so defer this one.
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

	// A missing data volume would be recreated empty; pipeline/webhook deploys reach here without the API's guard.
	if err := datavolume.CheckApp(ctx, h.clients, h.volumes, app); err != nil {
		h.log(dep, "refusing to deploy: "+err.Error())
		_ = h.fail(dep, err)
		return
	}

	// Internal pulls use a platform credential valid for every namespace, so an unchecked ref leaks other tenants' images.
	if ref, err := h.authorizedImageRef(app, dep.Image); err != nil {
		_ = h.fail(dep, err)
		return
	} else if ref != dep.Image {
		// Canonicalized to ws_<id>, which survives a workspace rename.
		dep.Image = ref
		_ = h.deployments.Update(dep)
	}

	var image string
	// Buildpack images run via the CNB launcher: no custom command, and $PORT is injected.
	var buildMethod models.AppBuildMethod
	switch {
	case dep.ImageID != nil && dep.Image != "" && h.imagePresent(ctx, app, dep.Image):
		// A pipeline-produced deploy must reproduce the exact commit+digest the run captured, so no rebuild or pull.
		image = dep.Image
		h.log(dep, "using prebuilt image "+image+" (no rebuild, no pull)")
	case dep.Image != "" && h.distributor != nil && h.distributor.IsBuildRef(dep.Image):
		// Pulling from the internal registry is what lets a Git-built app deploy or roll back on another node.
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
		// "auto" is resolved by the runner and handled safely in the runtime spec below.
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
				// This worker doesn't hold the runner tunnels; hand off to the control-plane worker that does.
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
			// The pre-flight only checked dep.Image, so the fallback image must be authorized on the same terms.
			ref, err := h.authorizedImageRef(app, app.ImageRef(""))
			if err != nil {
				_ = h.fail(dep, err)
				return
			}
			image = ref
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
		// A present digest-pinned ref is never re-pulled, but only where the workspace may reuse the cache (mayReuseCachedImage).
		present := h.imagePresent(ctx, app, image)
		pinned := strings.Contains(image, "@")
		internal := h.distributor != nil && h.distributor.IsBuildRef(image)
		reusable := mayReuseCachedImage(image, internal, auth)
		switch {
		case present && reusable && (pinned || app.ImagePullPolicy == models.PullIfNotPresent):
			h.log(dep, "image "+image+" already present — skipping pull")
		case app.ImagePullPolicy == models.PullNever:
			if !present {
				_ = h.fail(dep, fmt.Errorf("image %s is not present locally and pull policy is 'never'", image))
				return
			}
			// 'never' has no pull to fall back on, so an unauthorized cached image is refused outright.
			if !reusable {
				_ = h.fail(dep, fmt.Errorf("%s: %w", image, ErrCachedImageUnauthorized))
				return
			}
			h.log(dep, "image "+image+" present — skipping pull (policy: never)")
		default: // always, if-not-present with the image absent, or a cached image this workspace cannot claim
			h.log(dep, "pulling image "+image)
			if err := h.eng(app).PullImage(ctx, image, auth); err != nil {
				_ = h.fail(dep, fmt.Errorf("pull image: %w", err))
				return
			}
			h.log(dep, "image pulled")
		}
	}

	// Device binding happens later once the node's runtime is known; the restricted-profile conflict is checked there too.
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

	if app.RuntimeKind == models.RuntimeService {
		h.deployService(ctx, app, dep, image, buildMethod)
		return
	}

	h.log(dep, "creating container")
	dep.Status = models.DeploymentDeploying
	_ = h.deployments.Update(dep)
	h.publishStatus(dep, models.DeploymentDeploying)

	canary := dep.Strategy == models.DeployCanary
	// The canary answers its own alias so the proxy can split weighted traffic between it and the stable release.
	upstreamAlias := node.AppAlias(app)
	if canary {
		upstreamAlias = node.CanaryAlias(app)
	}
	// Routing uses the alias, not the container name.
	name := fmt.Sprintf("%s-%d", node.AppAlias(app), dep.ID)
	// The same builder a one-off Job uses, so they can't drift.
	rc, err := h.buildRuntimeContext(ctx, h.eng(app), app)
	if err != nil {
		_ = h.fail(dep, fmt.Errorf("sync networks: %w", err))
		return
	}
	// Buildpack images run the CNB launcher and read $PORT, so the command is never overridden.
	cmd := app.Command
	switch buildMethod {
	case models.BuildBuildpack:
		cmd = nil
		rc.Env = ensurePortEnv(rc.Env, app.Port)
	case models.BuildAuto:
		// The runner resolved auto to either method; injecting $PORT is required by buildpacks and harmless to Dockerfiles.
		rc.Env = ensurePortEnv(rc.Env, app.Port)
	}
	// A Job never registers this alias: it must not impersonate the service.
	aliasesByNet := nameAliasesByNetwork(app, canary)
	// A canary shares the host with the stable container, so it must not re-publish the same host ports.
	ports := map[string]string{}
	portIPs := map[string]string{}
	if h.portBindings != nil && !canary {
		if approved, err := h.portBindings.ListApprovedByApp(app.ID); err == nil {
			for _, b := range approved {
				key := fmt.Sprintf("%d/%s", b.ContainerPort, b.Protocol)
				if h.portPolicy != nil {
					d := h.portPolicy.CheckPortPublish(secpolicy.PortPublish{
						WorkspaceID: app.WorkspaceID, Binding: b,
						Resource: fmt.Sprintf("app:%d %d/%s", app.ID, b.HostPort, b.Protocol),
					})
					if !d.Allowed {
						h.log(dep, d.Reason)
						continue
					}
					if d.BindAddress != "" {
						portIPs[key] = d.BindAddress
					}
				}
				ports[key] = fmt.Sprintf("%d", b.HostPort)
			}
		}
	}
	if eff := effectiveStrategy(dep.Strategy, len(ports)); eff != dep.Strategy {
		published := make([]string, 0, len(ports))
		for key := range ports {
			published = append(published, key)
		}
		sort.Strings(published)
		h.log(dep, fmt.Sprintf("host port %s is published — rolling would collide on it, using recreate (brief downtime)",
			strings.Join(published, ", ")))
		dep.Strategy = eff
		_ = h.deployments.Update(dep)
	}

	// Recreate accepts brief downtime; required when versions can't coexist.
	if dep.Strategy == models.DeployRecreate {
		if prev, err := h.releases.FindActive(app.ID); err == nil && prev.ContainerID != "" {
			h.log(dep, "recreate: stopping current release v"+fmt.Sprint(prev.Version))
			h.retireRelease(context.Background(), app, prev)
		}
	}

	// Managed volumes are chowned to the run-as user so the container can still write to them.
	sec, secErr := h.workloadSecurity(app, app.RunAsUser)
	if secErr != nil {
		_ = h.fail(dep, secErr)
		return
	}
	if sec.HasUser() {
		if sec.Restricted {
			h.log(dep, "security profile: restricted (running as "+sec.User+")")
		} else {
			h.log(dep, "running as "+sec.User)
		}
		if err := h.prepareVolumeOwnership(ctx, h.eng(app), sec, image, rc.Mounts); err != nil {
			_ = h.fail(dep, fmt.Errorf("prepare volumes: %w", err))
			return
		}
	}

	// Device passthrough is privileged, so it cannot coexist with the restricted profile; refuse rather than drop either.
	var gpuReqs []docker.GPURequest
	if app.GPUCount > 0 && h.gpu != nil {
		if sec.Restricted {
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
		Files:    rc.Files,
		// Volumes are already chowned to the pinned user; copy-up would re-apply the image's ownership and undo it.
		NoCopyVolumes:    sec.HasUser(),
		Binds:            rc.Binds,
		Networks:         rc.Networks,
		Ports:            ports,
		PortBindIPs:      portIPs,
		NetworkAliases:   []string{upstreamAlias}, // upstream alias for the proxy (stable or canary)
		AliasesByNetwork: aliasesByNet,
		MemoryBytes:      rc.MemoryBytes,
		NanoCPUs:         rc.NanoCPUs,
		GPUs:             gpuReqs,
		RestartPolicy:    string(app.RestartPolicy),
		Healthcheck:      buildHealthcheck(app),
		Labels:           containerLabels(app, dep.ID),
	}
	if spec.Healthcheck != nil && app.HealthcheckType == models.HealthcheckHTTP {
		if info, err := h.eng(app).Info(ctx); err == nil {
			spec.Files = withBinaryHTTPProbe(app, spec.Healthcheck, spec.Files, info.Arch, sec.ReadOnlyRootfs)
		}
	}
	sec.applyTo(&spec)
	containerID, err := h.eng(app).RunContainer(ctx, spec)
	if err != nil {
		// A failed start leaves a created container; remove it so a port conflict doesn't leave an orphan.
		if containerID != "" {
			_ = h.eng(app).RemoveContainer(context.Background(), containerID, true)
		}
		_ = h.fail(dep, fmt.Errorf("run container: %w", err))
		return
	}
	dep.ContainerID = containerID
	_ = h.deployments.Update(dep)
	h.log(dep, "container created "+shortID(containerID))

	if app.HealthcheckType.GatesDeploy() {
		h.log(dep, "waiting for container to become healthy")
	}
	if err := h.healthGate(ctx, app, containerID); err != nil {
		_ = h.eng(app).RemoveContainer(context.Background(), containerID, true)
		_ = h.fail(dep, err)
		return
	}
	if app.HealthcheckType.GatesDeploy() {
		h.log(dep, "container is healthy")
	}

	if canary {
		h.releaseCanary(app, dep, image, containerID)
		return
	}
	h.swapAndRelease(app, dep, image, containerID)
}

func (h *DeployHandler) manager(ctx context.Context, app *models.Application) docker.Client {
	dc, err := h.clients.For(0)
	if h.cluster != nil {
		dc, err = h.cluster.Manager(ctx, app.ClusterID)
	}
	if err != nil {
		return docker.Offline(err)
	}
	return dc
}

// endpointMode is how the app's cluster reaches its services by name.
func (h *DeployHandler) endpointMode(app *models.Application) string {
	if h.cluster == nil {
		return string(models.ServiceEndpointVIP)
	}
	return string(h.cluster.ServiceEndpointMode(app.ClusterID))
}

// A node-local bridge cannot attach to a service, so a workspace still on bridges is a hard failure.
func (h *DeployHandler) serviceNetworks(ctx context.Context, mgr docker.Client, app *models.Application) ([]string, error) {
	if len(app.Networks) == 0 {
		return nil, fmt.Errorf("app %q has no workspace network to attach to", app.Name)
	}
	out := make([]string, 0, len(app.Networks))
	for i := range app.Networks {
		n := app.Networks[i]
		overlay := n.Driver == network.DriverOverlay
		if h.cluster != nil {
			overlay = h.cluster.WorkspaceOverlay(app.ClusterID, n)
		}
		if !overlay {
			return nil, fmt.Errorf(
				"workspace network %q is a node-local bridge, which a replicated service cannot join. "+
					"Enable cluster networking so workspace networks become overlays — otherwise this service "+
					"could not reach its databases", n.Name)
		}
		// Carve the subnet from the Miabi pool so swarm's own address pool can't exhaust.
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

// The service joins its workspace overlay for east-west traffic and the shared ingress overlay for the gateway.
func (h *DeployHandler) deployService(ctx context.Context, app *models.Application, dep *models.Deployment, image string, buildMethod models.AppBuildMethod) {
	mgr := h.manager(ctx, app)
	sw, err := mgr.Swarm(ctx)
	if err != nil || sw.LocalNodeState != "active" || !sw.ControlAvailable {
		_ = h.fail(dep, fmt.Errorf("cluster mode is not enabled; cannot deploy %q as a swarm service", app.Name))
		return
	}

	h.log(dep, "creating service")
	dep.Status = models.DeploymentDeploying
	_ = h.deployments.Update(dep)
	h.publishStatus(dep, models.DeploymentDeploying)

	// A service-only network would leave the service unable to see its own database.
	svcNets, nerr := h.serviceNetworks(ctx, mgr, app)
	if nerr != nil {
		_ = h.fail(dep, nerr)
		return
	}

	// Joined only while a route exposes the app; ProxyNetworkReconciler attaches it live when a route is added later.
	ingressNet := ""
	if h.appHasRoutes(app.ID) {
		if _, err := mgr.CreateOverlayNetwork(ctx, node.IngressOverlay); err != nil {
			_ = h.fail(dep, fmt.Errorf("ensure ingress overlay network: %w", err))
			return
		}
		ingressNet = node.IngressOverlay
	}

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
	// Swarm distributes configs over raft so a task gets its files on any node; per file so the image's path contents survive.
	svcConfigs, cfgErr := h.publishConfigs(ctx, h.eng(app), app, fmt.Sprint(app.WorkspaceID), env)
	if cfgErr != nil {
		_ = h.fail(dep, fmt.Errorf("publish config files: %w", cfgErr))
		return
	}

	// Host-preset binds aren't supported since tasks may land on any node; shared volumes carry their driver config.
	mounts := map[string]string{}
	mountDrivers := map[string]docker.ServiceMountDriver{}
	var binds []docker.ServiceBind
	for _, m := range app.Mounts {
		if m.HostPreset != "" {
			continue // preset host binds aren't supported on services
		}
		if m.ConfigID != 0 {
			continue // attached as config objects above
		}
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
	// Without these credentials a private image pulls on the manager but fails on every worker.
	var regAuth *docker.RegistryAuth
	if h.distributor != nil && h.distributor.IsBuildRef(image) {
		regAuth = h.distributor.PushAuth()
	} else if a, err := h.resolveRegistryAuth(app, dep); err != nil {
		h.log(dep, "WARN: could not resolve registry auth for workers: "+err.Error())
	} else {
		regAuth = a
	}
	// A stable service name lets redeploys update the same service in place.
	alias := node.AppAlias(app)
	sec, secErr := h.workloadSecurity(app, app.RunAsUser)
	if secErr == nil && len(sec.Devices) > 0 {
		secErr = models.ErrDevicesOnService
	}
	if secErr != nil {
		_ = h.fail(dep, secErr)
		return
	}
	spec := docker.ServiceSpec{
		Name:            alias,
		Image:           image,
		Env:             env,
		Cmd:             cmd,
		Replicas:        replicas,
		Networks:        svcNets,
		NetworkAliases:  []string{alias, app.Name},
		IngressNetwork:  ingressNet,
		IngressAlias:    alias,
		EndpointMode:    h.endpointMode(app),
		Mounts:          mounts,
		MountDrivers:    mountDrivers,
		Binds:           binds,
		Configs:         svcConfigs,
		MemoryBytes:     app.MemoryBytes,
		NanoCPUs:        app.NanoCPUs,
		Constraints:     h.serviceConstraints(app),
		Healthcheck:     buildHealthcheck(app),
		User:            sec.User,
		CapAdd:          sec.CapAdd,
		CapDrop:         sec.CapDrop,
		NoNewPrivileges: sec.NoNewPrivileges,
		ReadOnlyRootfs:  sec.ReadOnlyRootfs,
		Labels:          containerLabels(app, dep.ID),
		RegistryAuth:    regAuth,
	}
	if app.UpdateConfig != nil {
		if app.UpdateConfig.Parallelism > 0 {
			spec.UpdateParallelism = uint64(app.UpdateConfig.Parallelism)
		}
		if app.UpdateConfig.DelaySeconds > 0 {
			spec.UpdateDelay = time.Duration(app.UpdateConfig.DelaySeconds) * time.Second
		}
	}

	// The container goes first so the two never hold the same node-local volume at once.
	if dep.Strategy == models.DeployRecreate {
		if prev, err := h.releases.FindActive(app.ID); err == nil && prev.RuntimeKind == models.RuntimeContainer && prev.ContainerID != "" {
			h.log(dep, "recreate: stopping current container release v"+fmt.Sprint(prev.Version))
			h.retireRelease(ctx, app, prev)
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

	// An empty ID would strand the service for later scale/restart/remove, so fall back to the alias.
	serviceID := alias
	if st, err := mgr.ServiceInspect(ctx, alias); err == nil && st.ID != "" {
		serviceID = st.ID
	} else if err != nil {
		h.log(dep, fmt.Sprintf("warning: could not inspect service %s (%v); recording by name", alias, err))
	}
	h.releaseService(app, dep, image, serviceID)
}

// Shared volumes need the driver config so every node mounts the real share instead of an empty local volume.
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

	if err := storagesvc.SharedDriverOptsValid(opts); err != nil {
		logger.Error("refusing to mount a shared volume with unsafe driver options",
			"workspace", workspaceID, "volume", volumeID, "error", err)
		return nil
	}
	// nfs/cifs volumes use Docker's local driver with mount options, so the swarm mount does too.
	return &docker.ServiceMountDriver{Name: models.VolumeDriverLocal, Options: opts}
}

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

// A previous release is retired only when the app just switched from the container runtime.
func (h *DeployHandler) releaseService(app *models.Application, dep *models.Deployment, image, serviceID string) {
	prev, _ := h.releases.FindActive(app.ID)
	version, _ := h.releases.NextVersion(app.ID)
	rel := &models.Release{
		ApplicationID: app.ID, DeploymentID: dep.ID, Version: version, RuntimeKind: models.RuntimeService,
		Image: image, ContainerID: serviceID, Active: true,
		Commit: dep.Commit, ImageID: dep.ImageID, ImageTag: h.releaseImageTag(app.ID, dep, image),
		PipelineRunID: dep.PipelineRunID,
	}
	if err := h.releases.Create(rel); err != nil {
		_ = h.fail(dep, fmt.Errorf("create release: %w", err))
		return
	}
	_ = h.releases.Activate(app.ID, rel.ID)
	h.releaseActivated(app.ID, rel.ID)

	finished := time.Now()
	dep.Status = models.DeploymentSucceeded
	dep.FinishedAt = &finished
	_ = h.deployments.Update(dep)
	if app.SourceType != models.AppSourceGit && image != "" && !isPinnedRef(app, image) {
		img, tag := models.SplitImageRef(image)
		_ = h.apps.SetCurrentReleaseImage(app.ID, rel.ID, models.AppStatusRunning, img, tag)
	} else {
		_ = h.apps.SetCurrentRelease(app.ID, rel.ID, models.AppStatusRunning)
	}

	// Re-sync the route on every deploy so a redeploy restores routing rather than trusting the last route change.
	if h.routes != nil {
		if err := h.routes.SyncRoute(context.Background(), app.ID); err != nil {
			h.log(dep, "WARN: failed to sync proxy route: "+err.Error())
		} else {
			h.log(dep, "proxy routes synced")
		}
	}

	if prev != nil && prev.RuntimeKind == models.RuntimeContainer && prev.ContainerID != "" {
		h.log(dep, fmt.Sprintf("draining previous container release for %s", routeDrainDelay))
		time.Sleep(routeDrainDelay)
		h.log(dep, "retiring previous container release v"+fmt.Sprint(prev.Version))
		h.retireRelease(context.Background(), app, prev)
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
		Image: image, ContainerID: containerID, Active: false, RuntimeKind: models.RuntimeContainer,
		Commit: dep.Commit, ImageID: dep.ImageID, ImageTag: h.releaseImageTag(app.ID, dep, image),
		PipelineRunID: dep.PipelineRunID,
	}
	if err := h.releases.Create(rel); err != nil {
		_ = h.fail(dep, fmt.Errorf("create canary release: %w", err))
		return
	}
	h.tagReleaseImage(app, dep, version)

	weight := canaryInitialWeight(app)
	_ = h.apps.SetCanary(app.ID, &rel.ID, weight)
	_ = h.apps.SetCanaryPaused(app.ID, nil)
	// Both stable and canary are now serving — the app is running, not "deploying".
	_ = h.apps.SetStatus(app.ID, models.AppStatusRunning)

	// Stays non-terminal so the log keeps streaming rollout lines until finalizeCanary.
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

// Older rows may have zero values, hence the fallbacks.
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

func nextCanaryWeight(current, step int) int {
	n := current + step
	if n > 100 {
		return 100
	}
	return n
}

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

// detachCanary returns the canary release for retiring only once the route sync stops sending it traffic.
func (h *DeployHandler) detachCanary(app *models.Application) *models.Release {
	cur, err := h.apps.FindByID(app.ID)
	if err != nil || cur.CanaryReleaseID == nil {
		return nil
	}
	rel, err := h.releases.FindByID(*cur.CanaryReleaseID)
	_ = h.apps.SetCanary(app.ID, nil, 0)
	if err != nil {
		return nil
	}
	return rel
}

func (h *DeployHandler) retireCanaryRelease(app *models.Application, dep *models.Deployment, rel *models.Release) {
	h.log(dep, "retiring canary release v"+fmt.Sprint(rel.Version))
	if rel.ContainerID != "" {
		_ = h.eng(app).StopContainer(context.Background(), rel.ContainerID, 10)
		_ = h.eng(app).RemoveContainer(context.Background(), rel.ContainerID, true)
	}
	h.finalizeCanary(rel.DeploymentID, models.DeploymentSucceeded, fmt.Sprintf("canary superseded by deployment #%d", dep.Number))
	_ = h.releases.Delete(rel.ID)
}

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

	action, next := nextCanaryRamp(app)
	if action == canaryRampPromote {
		h.autoPromoteCanary(app, rel)
		return nil
	}
	if action == canaryRampAdvance {
		_ = h.apps.SetCanary(app.ID, app.CanaryReleaseID, next)
		if h.routes != nil {
			_ = h.routes.SyncRoute(context.Background(), app.ID)
		}
		h.recordCanary(app, rel.DeploymentID, models.SeverityInfo, fmt.Sprintf("Canary advanced to %d%% traffic", next))
		logger.Info("canary advanced", "app", app.ID, "weight", next)
	}
	// Reschedule even when holding, so a manual canary keeps its health gate and switching back resumes the ramp.
	if h.producer != nil {
		_ = h.producer.EnqueueCanaryStep(p.DeploymentID, canaryInterval(app), app.ServerID)
	}
	return nil
}

// canaryRampAction is what the rollout timer should do on a tick.
type canaryRampAction int

const (
	// canaryRampAdvance moves the weight to the returned value.
	canaryRampAdvance canaryRampAction = iota
	// canaryRampPromote ends the rollout: the ramp reached 100%.
	canaryRampPromote
	// canaryRampHold leaves the weight alone — manual mode, where the user owns it.
	canaryRampHold
)

// A manual-mode app is never stepped: its weight moves only when someone moves it.
func nextCanaryRamp(app *models.Application) (canaryRampAction, int) {
	if app.CanaryPausedAt != nil || app.CanaryMode == models.CanaryModeManual {
		return canaryRampHold, app.CanaryWeight
	}
	next := nextCanaryWeight(app.CanaryWeight, canaryStep(app))
	if next >= 100 {
		return canaryRampPromote, 100
	}
	return canaryRampAdvance, next
}

func canaryAppID(h *DeployHandler, deploymentID uint) uint {
	dep, err := h.deployments.FindByID(deploymentID)
	if err != nil {
		return 0
	}
	return dep.ApplicationID
}

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

// System labels are stamped last so they always win a key collision with user labels.
func containerLabels(app *models.Application, deploymentID uint) map[string]string {
	labels := docker.SanitizeUserLabels(app.ContainerLabels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[docker.LabelApp] = fmt.Sprintf("%d", app.ID)
	labels[docker.LabelDeployment] = fmt.Sprintf("%d", deploymentID)
	return stackLabels(app, labels)
}

// The workspace label scopes the node-container view; Compose labels let native tooling group stack containers.
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

var ErrForeignImage = errors.New("image is not available to this workspace")

// Built-in-registry pulls use the platform token every namespace accepts, so the ref is the only tenant boundary.
func (h *DeployHandler) authorizedImageRef(app *models.Application, ref string) (string, error) {
	if h.distributor == nil || ref == "" {
		return ref, nil
	}
	resolved, err := h.distributor.ResolveImageRef(app.WorkspaceID, ref)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrForeignImage, err)
	}
	return resolved, nil
}

// Folding "@sha256:..." into Image would outlive the pin and freeze later deploys on it.
func isPinnedRef(app *models.Application, ref string) bool {
	pin := app.Metadata[models.MetaDigest]
	return pin != "" && strings.HasSuffix(ref, "@"+pin)
}

// Errors report absent so the caller falls back to building or pulling.
func (h *DeployHandler) imagePresent(ctx context.Context, app *models.Application, ref string) bool {
	ok, err := h.eng(app).ImageExists(ctx, ref)
	return err == nil && ok
}

var ErrRegistryRequired = errors.New("git-source deploys require the internal registry (set MIABI_REGISTRY_ENABLED): the runner builds the image and pushes it there for the node to pull")

// This worker lacks runner tunnels, so the deploy is handed off to the control-plane worker rather than failed.
var errBuildDispatchUnavailable = errors.New("runner build dispatch is not configured on this worker")

const deployRunnerWaitInterval = 15 * time.Second

// ErrNoRunner / ErrRunnerOffline are returned unchanged so the caller can queue the deploy.
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
	// h.registryHost is empty when the host is derived, so take the host from Repository to log into the push host.
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
		Branch:           app.GitRef,
		Repository:       repository,
		Registry:         registryHost,
		Build:            h.buildConfigFromApp(app, dep, repository),
	}, 0, func(line string) { h.log(dep, line) })
	if err != nil {
		return "", err
	}
	// The build repopulated the current cache generation, so the next one need not run cold.
	if h.apps != nil {
		if err := h.apps.MarkCacheBuilt(app.ID, app.CacheGeneration); err != nil {
			logger.Warn("mark build cache generation failed", "app", app.ID, "error", err)
		}
	}
	imageRef := repository + "@" + digest
	h.recordBuiltImage(app, dep, repository, BuildResult{Digest: digest, Runner: "runner"})
	return imageRef, nil
}

// Builder image: the app's override, else the platform default; the runner uses its own only when both are empty.
func (h *DeployHandler) buildConfigFromApp(app *models.Application, dep *models.Deployment, repository string) *proto.BuildConfig {
	builder := app.Builder
	// Drop a custom builder the workspace is no longer entitled to, e.g. after a plan downgrade.
	if builder != "" && h.builderPolicy != nil && !h.builderPolicy.CustomBuilderAllowed(app.WorkspaceID) {
		builder = ""
	}
	if builder == "" && h.build.resolver != nil {
		builder = h.build.resolver.Ref(platformimage.KeyBuildpackBuilder)
	}

	cacheFrom, cacheTo := runners.CacheRefs(repository, app.GitRef, app.GitRef, app.CacheGeneration)
	noCache := (dep != nil && dep.NoCache) || app.CacheBuiltGeneration != app.CacheGeneration
	return &proto.BuildConfig{
		Method:     string(app.BuildMethod),
		Builder:    builder,
		Buildpacks: app.Buildpacks,
		BuildEnv:   app.BuildEnv,
		NoCache:    noCache,
		CacheFrom:  cacheFrom,
		CacheTo:    cacheTo,
	}
}

// The HTTPS credential is embedded because the runner has no local git auth; SSH-key credentials aren't supported.
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
	return gitrepo.CredentialURL(rawURL, gr, h.secrets)
}

// Fails after runnerWaitTimeout so a git build never runs on a node and a missing runner doesn't wait forever.
func (h *DeployHandler) deferForRunner(dep *models.Deployment, serverID uint, reason string) {
	// Only the latest deployment should wait for a runner, so older pending ones are failed as superseded.
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

// No-op for external images or when distribution is off.
func (h *DeployHandler) tagReleaseImage(app *models.Application, dep *models.Deployment, version int) {
	// A versioned build already carries its own tag; a v<release> tag beside v1.4.2 would only confuse.
	if dep.ImageTag != "" {
		return
	}
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

// Stored on dep too, so tagReleaseImage leaves a versioned image alone.
func (h *DeployHandler) releaseImageTag(appID uint, dep *models.Deployment, image string) string {
	if dep.ImageTag == "" && image != "" && h.releases != nil {
		if tag, err := h.releases.ImageTagByImage(appID, image); err == nil {
			dep.ImageTag = tag
		}
	}
	return dep.ImageTag
}

// Best-effort: a recording failure must not fail an otherwise-successful deploy.
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

// A per-deploy RegistryID takes precedence over the app's default; both nil means an anonymous pull.
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
	password, err := h.credentialSecret(app.WorkspaceID, reg.Secret, reg.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("registry %q: %w", reg.Name, err)
	}
	return &docker.RegistryAuth{Server: reg.Server, Username: reg.Username, Password: password}, nil
}

func (h *DeployHandler) healthGate(ctx context.Context, app *models.Application, containerID string) error {
	requireHealthy := app.HealthcheckType.GatesDeploy()
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

// Derived from the probe's timing so slow starters aren't cut off, but capped so deploys can't hang.
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

// nil keeps the image's HEALTHCHECK; none sends NONE so a baked-in web check cannot kill a worker.
func buildHealthcheck(app *models.Application) *docker.HealthcheckSpec {
	var test []string
	switch app.HealthcheckType {
	case models.HealthcheckHTTP:
		port, path := httpCheckTarget(app)
		test = []string{"CMD-SHELL", shellHTTPProbe(port, path)}
	case models.HealthcheckCommand:
		cmd := strings.TrimSpace(app.HealthcheckCommand)
		if cmd == "" {
			return nil
		}
		test = []string{"CMD-SHELL", cmd}
	case models.HealthcheckNone:
		return &docker.HealthcheckSpec{Test: []string{"NONE"}}
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

// httpCheckTarget resolves the port and path an HTTP healthcheck probes.
func httpCheckTarget(app *models.Application) (int, string) {
	port := app.HealthcheckPort
	if port == 0 {
		port = app.Port
	}
	if port == 0 {
		port = 80
	}
	path := strings.TrimSpace(app.HealthcheckHTTPPath)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return port, path
}

// Tries curl, wget or bash so slim images pass; shell-less images need withBinaryHTTPProbe.
func shellHTTPProbe(port int, path string) string {
	url := shellQuote(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	devTCP := fmt.Sprintf(`exec 3<>/dev/tcp/127.0.0.1/%d && printf "GET %%s HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n" "$1" >&3 && read -r _ c _ <&3 && [ "$c" -ge 200 ] && [ "$c" -lt 400 ]`, port)
	return "if command -v curl >/dev/null 2>&1; then curl -fsS -o /dev/null " + url +
		"; elif command -v wget >/dev/null 2>&1; then wget -q -O /dev/null " + url +
		"; elif command -v bash >/dev/null 2>&1; then bash -c " + shellQuote(devTCP) + " _ " + shellQuote(path) +
		"; else echo 'no curl, wget or bash in the image: use a command healthcheck' >&2; exit 1; fi"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Skipped without a binary for the node's arch, or on a read-only rootfs, which Docker refuses to copy into.
func withBinaryHTTPProbe(app *models.Application, hc *docker.HealthcheckSpec, files []docker.FileEntry, arch string, readOnlyRootfs bool) []docker.FileEntry {
	if hc == nil || app.HealthcheckType != models.HealthcheckHTTP || readOnlyRootfs {
		return files
	}
	bin, ok := healthprobe.Binary(arch)
	if !ok {
		return files
	}
	port, path := httpCheckTarget(app)
	hc.Test = []string{"CMD", healthprobe.Path, strconv.Itoa(port), path}
	return append(slices.Clip(files), docker.FileEntry{Path: healthprobe.Path, Content: string(bin), Mode: "0755"})
}

func hcInterval(app *models.Application) int {
	return clampInt(app.HealthcheckIntervalSeconds, 1, 3600)
}
func hcTimeout(app *models.Application) int { return clampInt(app.HealthcheckTimeoutSeconds, 1, 3600) }
func hcRetries(app *models.Application) int { return clampInt(app.HealthcheckRetries, 1, 20) }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// The app's runtime must name what actually runs, or lifecycle ops would target the wrong kind of workload.
func (h *DeployHandler) revertRuntimeSwitch(app *models.Application, dep *models.Deployment) {
	active, err := h.releases.FindActive(app.ID)
	if err != nil || active.RuntimeKind == "" || active.RuntimeKind == app.RuntimeKind {
		return
	}
	if err := h.apps.SetRuntimeKind(app.ID, active.RuntimeKind); err != nil {
		logger.Warn("failed to revert the runtime switch", "app", app.ID, "error", err)
		return
	}
	h.log(dep, fmt.Sprintf("runtime switch failed; the app stays on the %s runtime", active.RuntimeKind))
	h.emit(app, dep, models.EventSettingsUpdated, models.SeverityWarning,
		fmt.Sprintf("Runtime switch to %s failed; the app keeps running as a %s", app.RuntimeKind, active.RuntimeKind))
}

// A service release's ContainerID is a Swarm service id, which container calls would silently fail to remove.
func (h *DeployHandler) retireRelease(ctx context.Context, app *models.Application, rel *models.Release) {
	if rel.RuntimeKind == models.RuntimeService {
		if err := h.manager(ctx, app).ServiceRemove(ctx, rel.ContainerID); err != nil {
			logger.Warn("failed to remove the previous service release", "app", app.ID, "release", rel.ID, "error", err)
		}
		return
	}
	_ = h.eng(app).StopContainer(ctx, rel.ContainerID, 10)
	_ = h.eng(app).RemoveContainer(ctx, rel.ContainerID, true)
}

// Goma reloads its file provider after a 500ms debounce and edge gateways pull async.
var routeDrainDelay = 5 * time.Second

func (h *DeployHandler) swapAndRelease(app *models.Application, dep *models.Deployment, image, containerID string) {

	prev, _ := h.releases.FindActive(app.ID)
	canary := h.detachCanary(app)

	version, _ := h.releases.NextVersion(app.ID)
	rel := &models.Release{
		ApplicationID: app.ID, DeploymentID: dep.ID, Version: version,
		Image: image, ContainerID: containerID, Active: true, RuntimeKind: models.RuntimeContainer,
		Commit: dep.Commit, ImageID: dep.ImageID, ImageTag: h.releaseImageTag(app.ID, dep, image),
		PipelineRunID: dep.PipelineRunID,
	}
	if err := h.releases.Create(rel); err != nil {
		if canary != nil {
			h.retireCanaryRelease(app, dep, canary)
		}
		_ = h.fail(dep, fmt.Errorf("create release: %w", err))
		return
	}
	_ = h.releases.Activate(app.ID, rel.ID)
	h.releaseActivated(app.ID, rel.ID)

	finished := time.Now()
	dep.Status = models.DeploymentSucceeded
	dep.FinishedAt = &finished
	_ = h.deployments.Update(dep)
	// Git apps build their own image and keep their source unchanged.
	if app.SourceType != models.AppSourceGit && image != "" && !isPinnedRef(app, image) {
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

	retirePrev := prev != nil && prev.ContainerID != "" && prev.ContainerID != containerID
	if retirePrev || canary != nil {
		h.log(dep, fmt.Sprintf("draining previous release for %s", routeDrainDelay))
		time.Sleep(routeDrainDelay)
	}
	if canary != nil {
		h.retireCanaryRelease(app, dep, canary)
	}
	if retirePrev {
		h.log(dep, "retiring previous release v"+fmt.Sprint(prev.Version))
		h.retireRelease(context.Background(), app, prev)
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

func effectiveStrategy(requested models.DeployStrategy, publishedHostPorts int) models.DeployStrategy {
	if requested == models.DeployRolling && publishedHostPorts > 0 {
		return models.DeployRecreate
	}
	return requested
}

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

	app, _ := h.apps.FindByID(dep.ApplicationID)
	hasCurrentRelease := app != nil && app.CurrentReleaseID != nil
	_ = h.apps.SetStatus(dep.ApplicationID, failedAppStatus(hasCurrentRelease, dep.Strategy))
	if app != nil && dep.Strategy != models.DeployRecreate {
		h.revertRuntimeSwitch(app, dep)
	}

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

// The DB tail stays as a fallback on any store error.
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

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (h *DeployHandler) logTo(deploymentID uint, line string) {
	_ = h.deployments.AppendLog(deploymentID, line)
	h.bus.Publish(DeployTopic(deploymentID), eventbus.Event{Type: "log", Data: line})
}

func (h *DeployHandler) publishStatus(dep *models.Deployment, status models.DeploymentStatus) {
	h.bus.Publish(DeployTopic(dep.ID), eventbus.Event{Type: "status", Data: string(status)})
}

// Per-network, never NetworkAliases: the shared proxy network spans every workspace, so a bare name would leak.
func nameAliasesByNetwork(app *models.Application, canary bool) map[string][]string {
	out := map[string][]string{}
	if app.Stack != nil && app.Stack.DockerNetwork != "" {
		out[app.Stack.DockerNetwork] = []string{app.Name}
	}
	// A sibling resolving the workspace alias must reach the stable release, not the canary.
	if canary {
		return out
	}
	for _, n := range app.Networks {
		if n.DockerName == "" {
			continue
		}
		out[n.DockerName] = appendUniqueAlias(out[n.DockerName], app.Name)
	}
	return out
}

// Docker refuses a duplicate alias on one attachment, and a stack network may also be a workspace network.
func appendUniqueAlias(list []string, name string) []string {
	for _, v := range list {
		if v == name {
			return list
		}
	}
	return append(list, name)
}
