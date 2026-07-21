// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package application manages applications, their config, and deployments.
package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/dotenv"
	"github.com/miabi-io/miabi/internal/hostmount"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/events"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/worker"
)

var (
	ErrSlugTaken          = errors.New("application name already taken")
	ErrNameInvalid        = errors.New("name must contain only lowercase letters, digits and hyphens")
	ErrImageRequired      = errors.New("image is required for image-source applications")
	ErrGitRepoRequired    = errors.New("git_repo is required for git-source applications")
	ErrBuildConfigOnImage = errors.New("build configuration is only valid for git-source applications")
	ErrInvalidBuildMethod = errors.New("invalid build method")
	ErrNotDeployable      = errors.New("application has no active release")
	ErrVolumeNotFound     = errors.New("volume not found in workspace")
	ErrEnvVarNotFound     = errors.New("environment variable not found")
	ErrMountPathRequired  = errors.New("mount path is required")
	ErrReleaseNotFound    = errors.New("release not found")
	ErrReleaseActive      = errors.New("cannot delete the active release")
	ErrReleasePinned      = errors.New("cannot delete a pinned release")
	ErrStackNotFound      = errors.New("stack not found in workspace")
	ErrAppRunning         = errors.New("stop the application before deleting it")
	ErrNodeMismatch       = errors.New("the volume is on a different node than this application")

	ErrUnknownHostPreset      = errors.New("unknown host mount preset")
	ErrHostMountNotPrivileged = errors.New("host mounts require a privileged workspace")

	ErrInvalidGPUCount = errors.New("gpu_count must be between 0 and 64")

	ErrClusterDisabled       = errors.New("cluster mode is not enabled; cannot run this application as a service")
	ErrNotService            = errors.New("this operation is only valid for service-runtime (cluster) applications")
	ErrLocalVolumeReplicated = errors.New("a replicated (replicas>1) service cannot mount a node-local volume; use a shared (nfs/cifs) volume or set replicas to 1")
	ErrHostBindService       = errors.New("a service-runtime app cannot use a privileged host mount; host paths are node-local and don't follow a rescheduled task — use the container runtime or a managed volume")
	ErrTooManyReplicas       = fmt.Errorf("replica count exceeds the maximum of %d", MaxReplicas)
	ErrPortRange             = errors.New("container port must be between 1 and 65535")
)

// MaxReplicas caps a service app's replica count so a single request can't ask
// Swarm to schedule an unbounded number of tasks (node resource-exhaustion DoS).
const MaxReplicas = 100

// CreateInput describes a new application.
type CreateInput struct {
	// DisplayName is the free-text label. Handle is the desired unique slug
	// (the URL/CLI handle); when blank it is derived from DisplayName.
	DisplayName string
	Handle      string
	ServerID    uint // node to place on (0 = local)
	SourceType  models.AppSourceType
	Icon        string // optional logo (URL or "mdi-…" class); set from a template
	Image       string
	Tag         string
	GitRepo     string
	GitRef      string
	// Build config (git source only). BuildMethod defaults to auto; Builder,
	// Buildpacks, and BuildEnv tune a buildpack build. Rejected for image apps.
	BuildMethod     models.AppBuildMethod
	Builder         string
	Buildpacks      []string
	BuildEnv        map[string]string
	RegistryID      *uint
	GitRepositoryID *uint
	StackID         *uint
	NetworkIDs      []uint
	Ports           []PortSpec
	Command         []string
	Port            int
	MemoryBytes     int64
	NanoCPUs        int64
	// GPUCount / GPUKind request whole GPU devices. Gated by the AllowGPU plan
	// capability at create time; 0 = none.
	GPUCount        int
	GPUKind         string
	RestartPolicy   models.RestartPolicy
	ImagePullPolicy models.ImagePullPolicy
	// Cluster runtime (cluster mode). RuntimeKind defaults to container; service
	// runs the app as a replicated Swarm service (rejected when cluster mode is
	// off). Replicas/PlacementConstraints/UpdateConfig apply to the service runtime.
	RuntimeKind          models.RuntimeKind
	Replicas             int
	PlacementConstraints []string
	UpdateConfig         *models.ServiceUpdateConfig
	// Metadata is the app's initial metadata. Callers may set reserved
	// "miabi.io/" keys (e.g. provenance); a missing managed-by defaults to
	// "user". HTTP handlers must sanitize user input before setting reserved keys.
	Metadata models.Metadata
	// Annotations is the app's initial annotations: free-form descriptive notes
	// with no reserved keys (the manifest's metadata.annotations).
	Annotations models.Metadata
	// ContainerLabels are user-defined Docker labels for the app's container(s)
	// (Traefik &c.). Reserved keys (io.miabi.*, com.docker.*) are sanitized on
	// create regardless of caller.
	ContainerLabels map[string]string
}

type Service struct {
	apps         *repositories.ApplicationRepository
	deployments  *repositories.DeploymentRepository
	releases     *repositories.ReleaseRepository
	volumes      *repositories.VolumeRepository
	routes       *repositories.RouteRepository
	networks     *repositories.NetworkRepository
	stacks       *repositories.StackRepository
	ports        *repositories.AppPortRepository
	appEvents    *repositories.AppEventRepository
	clients      NodeDocker
	producer     *worker.Producer
	events       events.Recorder
	routeSync    RouteSyncer
	settings     *settings.Provider
	nodeGuard    NodeGuard
	serverInfo   ServerInfo
	nodeNamer    NodeNamer
	workspaces   WorkspaceInfo
	portBindings *repositories.PortBindingRepository
	quota        *quota.Service
	cluster      ClusterCap
	netEnsurer   NetworkEnsurer
}

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// NetworkEnsurer resolves — creating if necessary — a workspace's default,
// platform-managed Docker network, so every app is guaranteed to join it even
// when the network was not provisioned at workspace-creation time (e.g. the
// Docker daemon was briefly unavailable then). Implemented by the network
// service; an interface keeps application decoupled from it.
type NetworkEnsurer interface {
	EnsureDefault(ctx context.Context, workspaceID uint) (*models.Network, error)
}

// SetNetworkEnsurer wires default-network self-healing (nil-safe). Without it,
// SetNetworks still attaches an already-existing default network, but cannot
// create a missing one.
func (s *Service) SetNetworkEnsurer(e NetworkEnsurer) { s.netEnsurer = e }

// ClusterCap reports whether the manager is a swarm manager (cluster mode on).
// Implemented by the cluster service; used to gate "service" runtime apps.
type ClusterCap interface {
	CapCluster() bool
}

// SetClusterCap wires the cluster-capability check (nil-safe; nil means cluster
// mode is treated as off, so service-runtime apps are rejected).
func (s *Service) SetClusterCap(c ClusterCap) { s.cluster = c }

// clusterEnabled reports whether cluster mode is currently available.
func (s *Service) clusterEnabled() bool {
	return s.cluster != nil && s.cluster.CapCluster()
}

// SetPortBindings wires the port-binding repository used by EnsurePublished to
// learn which host ports an app's container must publish (injected after
// construction).
func (s *Service) SetPortBindings(r *repositories.PortBindingRepository) { s.portBindings = r }

// ExternalLabelTaken reports whether another application already owns the given
// external-access label. The label maps to a platform-wide host, so it must be
// unique across all workspaces; exceptAppID excludes the app being reconciled.
func (s *Service) ExternalLabelTaken(label string, exceptAppID uint) (bool, error) {
	return s.apps.ExternalLabelTaken(label, exceptAppID)
}

// WorkspaceInfo resolves a workspace's trust flags (privileged). Implemented by
// the workspace repository; injected after construction. Used to gate
// privileged host mounts.
type WorkspaceInfo interface {
	FindByID(id uint) (*models.Workspace, error)
}

// SetWorkspaceInfo wires the resolver used to check a workspace's privileged flag.
func (s *Service) SetWorkspaceInfo(w WorkspaceInfo) { s.workspaces = w }

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
	// ForServiceTask finds the engine holding a swarm service's task container.
	// A service has no fixed node, and only the node running the task can see it.
	ForServiceTask(ctx context.Context, serviceName string) (docker.Client, string, error)
}

// NodeGuard validates that a node can accept a new placement (exists, not
// cordoned). Implemented by the node service; injected after construction.
type NodeGuard interface {
	Placeable(serverID uint) error
}

// ServerInfo resolves a node's display metadata by id. Implemented by the node
// service; injected after construction (optional — read paths degrade to an
// empty server name when unset).
type ServerInfo interface {
	Get(id uint) (*models.Server, error)
}

// SetNodeGuard wires the placement guard consulted when creating an app on a node.
func (s *Service) SetNodeGuard(g NodeGuard) { s.nodeGuard = g }

// SetServerInfo wires the resolver used to annotate apps with their node's name.
func (s *Service) SetServerInfo(si ServerInfo) { s.serverInfo = si }

// NodeNamer resolves a swarm node id to its Miabi display name, so a cluster
// app's real replica placement can be shown by node name. Implemented by the
// node service; injected after construction (optional — placement degrades to a
// short swarm id when unset).
type NodeNamer interface {
	NameBySwarmNodeID(swarmNodeID string) string
}

// SetNodeNamer wires the swarm-node-id → name resolver used to annotate a cluster
// app with where its replicas actually run.
func (s *Service) SetNodeNamer(n NodeNamer) { s.nodeNamer = n }

// annotateServer fills the transient ServerName from the node record so the UI
// can show where an app runs. Best-effort: leaves it empty on any error.
func (s *Service) annotateServer(app *models.Application) {
	if app == nil || s.serverInfo == nil {
		return
	}
	if srv, err := s.serverInfo.Get(app.ServerID); err == nil && srv != nil {
		app.ServerName = srv.Name
	}
}

// annotatePlacement fills a cluster (service) app's transient Nodes with where
// the Swarm scheduler actually placed its running tasks, resolving swarm node ids
// to Miabi node names. Best-effort and read-only: a no-op for container apps,
// when cluster mode is off, or when the manager/service is unreachable — the UI
// then falls back to the static ServerName. Only called on the app-detail read,
// so it never adds a Docker round-trip per app to list views.
func (s *Service) annotatePlacement(ctx context.Context, app *models.Application) {
	if app == nil || app.RuntimeKind != models.RuntimeService {
		return
	}
	if s.cluster == nil || !s.cluster.CapCluster() {
		return
	}
	mgr, err := s.clients.For(0)
	if err != nil {
		return
	}
	st, err := mgr.ServiceInspect(ctx, node.AppAlias(app))
	if err != nil || len(st.Placement) == 0 {
		return
	}
	placements := make([]models.NodePlacement, 0, len(st.Placement))
	for swarmID, tasks := range st.Placement {
		name := shortSwarmID(swarmID)
		if s.nodeNamer != nil {
			if n := s.nodeNamer.NameBySwarmNodeID(swarmID); n != "" {
				name = n
			}
		}
		placements = append(placements, models.NodePlacement{Name: name, Tasks: tasks})
	}
	// Stable, readable order: most-loaded node first, then by name.
	sort.Slice(placements, func(i, j int) bool {
		if placements[i].Tasks != placements[j].Tasks {
			return placements[i].Tasks > placements[j].Tasks
		}
		return placements[i].Name < placements[j].Name
	})
	app.Nodes = placements
}

// shortSwarmID trims a swarm node id to a readable prefix for display when it
// can't be correlated to a Miabi node name.
func shortSwarmID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// eng resolves the Docker client for an app's node, returning an offline client
// when the node's agent is disconnected (critical ops fail; cleanups no-op).
func (s *Service) eng(app *models.Application) docker.Client {
	dc, err := s.clients.For(app.ServerID)
	if err != nil {
		return docker.Offline(err)
	}
	return dc
}

// RouteSyncer reconciles an application's reverse-proxy routes. The route
// service implements it; injected after construction to avoid an import cycle.
type RouteSyncer interface {
	SyncRoute(ctx context.Context, appID uint) error
	// RemoveAppRoutes deletes all of an app's routes (generated + user) from the
	// database and the proxy, used when the app itself is deleted.
	RemoveAppRoutes(ctx context.Context, appID uint) error
}

// SetRouteSyncer wires the route reconciler used by canary traffic changes.
func (s *Service) SetRouteSyncer(rs RouteSyncer) { s.routeSync = rs }

// SetSettings wires the platform settings provider used to enforce resource caps.
func (s *Service) SetSettings(p *settings.Provider) { s.settings = p }

// ErrResourceCap is returned when a requested CPU/memory exceeds a platform cap.
var ErrResourceCap = errors.New("requested resources exceed the platform limit")

const bytesPerMB = 1024 * 1024

// validateResources rejects CPU/memory requests above the admin-configured caps
// (0 = unlimited). The error message names the offending limit for the UI.
func (s *Service) validateResources(memoryBytes, nanoCPUs int64) error {
	// Negative limits are meaningless (0 = unlimited) and would otherwise reach
	// the Docker API; reject them regardless of whether platform caps are set.
	if memoryBytes < 0 || nanoCPUs < 0 {
		return fmt.Errorf("%w: memory and CPU limits cannot be negative", ErrResourceCap)
	}
	if s.settings == nil {
		return nil
	}
	return checkResourceCaps(
		s.settings.Int(settings.KeyMaxMemoryMB, 0),
		s.settings.Int(settings.KeyMaxCPUCores, 0),
		memoryBytes, nanoCPUs,
	)
}

// checkResourceCaps returns ErrResourceCap when a request exceeds a non-zero
// cap. Pure (no provider) so it can be unit-tested directly.
func checkResourceCaps(maxMemoryMB, maxCPUCores int, memoryBytes, nanoCPUs int64) error {
	if maxMemoryMB > 0 && memoryBytes > int64(maxMemoryMB)*bytesPerMB {
		return fmt.Errorf("%w: requested %d MB of memory exceeds the platform limit of %d MB", ErrResourceCap, memoryBytes/bytesPerMB, maxMemoryMB)
	}
	if maxCPUCores > 0 && nanoCPUs > int64(maxCPUCores)*1_000_000_000 {
		return fmt.Errorf("%w: requested %.2f CPU cores exceeds the platform limit of %d", ErrResourceCap, float64(nanoCPUs)/1e9, maxCPUCores)
	}
	return nil
}

// ResourceLimits returns the platform per-app CPU/memory caps (0 = unlimited),
// for surfacing as hints in the UI.
func (s *Service) ResourceLimits() (maxCPUCores, maxMemoryMB int) {
	if s.settings == nil {
		return 0, 0
	}
	return s.settings.Int(settings.KeyMaxCPUCores, 0), s.settings.Int(settings.KeyMaxMemoryMB, 0)
}

// normalizeHealthcheck clamps healthcheck type and timing to safe values.
func normalizeHealthcheck(app *models.Application) {
	if !models.ValidHealthcheckType(app.HealthcheckType) {
		app.HealthcheckType = models.HealthcheckNone
	}
	if app.HealthcheckIntervalSeconds < 1 {
		app.HealthcheckIntervalSeconds = 30
	}
	if app.HealthcheckTimeoutSeconds < 1 {
		app.HealthcheckTimeoutSeconds = 5
	}
	if app.HealthcheckRetries < 1 {
		app.HealthcheckRetries = 3
	}
	if app.HealthcheckStartPeriodSeconds < 0 {
		app.HealthcheckStartPeriodSeconds = 0
	}
}

func NewService(
	apps *repositories.ApplicationRepository,
	deployments *repositories.DeploymentRepository,
	releases *repositories.ReleaseRepository,
	volumes *repositories.VolumeRepository,
	routes *repositories.RouteRepository,
	networks *repositories.NetworkRepository,
	stacks *repositories.StackRepository,
	ports *repositories.AppPortRepository,
	appEvents *repositories.AppEventRepository,
	clients NodeDocker,
	producer *worker.Producer,
	evts events.Recorder,
) *Service {
	return &Service{apps: apps, deployments: deployments, releases: releases, volumes: volumes, routes: routes, networks: networks, stacks: stacks, ports: ports, appEvents: appEvents, clients: clients, producer: producer, events: evts}
}

// validateStack confirms a referenced stack belongs to the workspace. A nil id
// (ungrouped) validates trivially.
func (s *Service) validateStack(workspaceID uint, stackID *uint) error {
	if stackID == nil {
		return nil
	}
	if _, err := s.stacks.FindInWorkspace(workspaceID, *stackID); err != nil {
		return ErrStackNotFound
	}
	return nil
}

// PortSpec describes a container port to declare on an application.
type PortSpec struct {
	ContainerPort int
	Protocol      string
	Scheme        string
	Name          string
}

// SetPorts replaces an application's declared container ports and keeps the
// primary Port field in sync with the first declared port.
func (s *Service) SetPorts(app *models.Application, specs []PortSpec) error {
	ports := make([]models.AppPort, 0, len(specs))
	for _, sp := range specs {
		if sp.ContainerPort <= 0 {
			continue
		}
		if sp.ContainerPort > 65535 {
			return ErrPortRange
		}
		proto := sp.Protocol
		if proto != "udp" {
			proto = "tcp"
		}
		scheme := "http"
		if sp.Scheme == "https" {
			scheme = "https"
		}
		ports = append(ports, models.AppPort{ContainerPort: sp.ContainerPort, Protocol: proto, Scheme: scheme, Name: sp.Name})
	}
	if err := s.ports.ReplaceForApp(app.ID, ports); err != nil {
		return err
	}
	primary := 0
	if len(ports) > 0 {
		primary = ports[0].ContainerPort
	}
	if primary != app.Port {
		app.Port = primary
		return s.apps.Update(app)
	}
	return nil
}

// Overview is an aggregated summary of an application for the detail page.
type Overview struct {
	Status         models.AppStatus     `json:"status"`
	SourceType     models.AppSourceType `json:"source_type"`
	Image          string               `json:"image,omitempty"`
	Tag            string               `json:"tag,omitempty"`
	GitRepo        string               `json:"git_repo,omitempty"`
	CurrentVersion int                  `json:"current_version"` // 0 = no active release
	CurrentImage   string               `json:"current_image,omitempty"`
	// Hostname is the app's stable internal DNS name, reachable from other
	// managed containers on the shared network across redeploys. StackHostname
	// is its service name within its stack network (empty when ungrouped).
	Hostname         string            `json:"hostname"`
	StackHostname    string            `json:"stack_hostname,omitempty"`
	RedeployRequired bool              `json:"redeploy_required"`
	VolumesCount     int               `json:"volumes_count"`
	RoutesCount      int               `json:"routes_count"`
	NetworksCount    int               `json:"networks_count"`
	EnvCount         int               `json:"env_count"`
	CreatedAt        time.Time         `json:"created_at"`
	RecentEvents     []models.AppEvent `json:"recent_events"`
}

// Overview aggregates summary fields and resource counts for an application.
func (s *Service) Overview(app *models.Application) *Overview {
	ov := &Overview{
		Status:           app.Status,
		SourceType:       app.SourceType,
		Image:            app.Image,
		Tag:              app.Tag,
		GitRepo:          app.GitRepo,
		Hostname:         node.AppAlias(app),
		RedeployRequired: app.RedeployRequired,
		VolumesCount:     len(app.Mounts),
		CreatedAt:        app.CreatedAt,
	}
	if app.StackID != nil {
		ov.StackHostname = app.Name // resolvable by sibling apps on the stack network
	}
	if rel, err := s.releases.FindActive(app.ID); err == nil {
		ov.CurrentVersion = rel.Version
		ov.CurrentImage = rel.Image
	}
	if envs, err := s.apps.ListEnvVars(app.ID); err == nil {
		ov.EnvCount = len(envs)
	}
	if n, err := s.routes.CountByApp(app.ID); err == nil {
		ov.RoutesCount = int(n)
	}
	if n, err := s.apps.CountNetworks(app.ID); err == nil {
		ov.NetworksCount = int(n)
	}
	if evts, err := s.appEvents.ListByApp(app.ID, 6, 0); err == nil {
		ov.RecentEvents = evts
	}
	return ov
}

// LiveStatus is the real-time status of an application's container, derived from
// Docker inspect (not the stored, deploy-time status). It also carries a live
// resource-usage snapshot when the container is running.
type LiveStatus struct {
	Status         string              `json:"status"` // running|restarting|unhealthy|starting|exited|stopped|paused|created|no_container
	ContainerState string              `json:"container_state,omitempty"`
	Health         string              `json:"health,omitempty"`
	Running        bool                `json:"running"`
	Restarting     bool                `json:"restarting"`
	RestartCount   int                 `json:"restart_count"`
	ExitCode       int                 `json:"exit_code"`
	StartedAt      string              `json:"started_at,omitempty"`
	UptimeSeconds  int64               `json:"uptime_seconds"`
	HasContainer   bool                `json:"has_container"`
	StoredStatus   models.AppStatus    `json:"stored_status"`
	Stats          *docker.StatsSample `json:"stats,omitempty"`
	// Networks lists the running container's in-network IPs (ephemeral).
	Networks []docker.ContainerNetwork `json:"networks,omitempty"`
	// Cluster (service) runtime: the desired replica count and how many tasks are
	// currently running. Zero/omitted for container apps.
	ServiceReplicas     uint64 `json:"service_replicas,omitempty"`
	ServiceRunningTasks uint64 `json:"service_running_tasks,omitempty"`
}

// LiveStatus inspects the app's active container and returns its real-time
// status (plus a stats snapshot when running). Falls back to the stored status
// when there is no running container.
func (s *Service) LiveStatus(ctx context.Context, app *models.Application) LiveStatus {
	ls := LiveStatus{Status: string(app.Status), StoredStatus: app.Status}
	if app.RuntimeKind == models.RuntimeService {
		return s.serviceLiveStatus(ctx, app, ls)
	}
	cid, err := s.activeContainerID(app.ID)
	if err != nil {
		return ls // no active release/container — report the stored status
	}
	c, err := s.eng(app).InspectContainer(ctx, cid)
	if err != nil {
		ls.Status = "no_container" // active release recorded but its container is gone
		return ls
	}
	ls.HasContainer = true
	ls.ContainerState = c.State
	ls.Health = c.Health
	ls.Running = c.State == "running"
	ls.Restarting = c.Restarting || c.State == "restarting"
	ls.RestartCount = c.RestartCount
	ls.ExitCode = c.ExitCode
	ls.StartedAt = c.StartedAt
	ls.Networks = c.Networks
	ls.Status = deriveLiveStatus(c.State, c.Health, ls.Restarting, app.Status == models.AppStatusStopped)

	if ls.Running && c.StartedAt != "" {
		if t, perr := time.Parse(time.RFC3339Nano, c.StartedAt); perr == nil {
			ls.UptimeSeconds = int64(time.Since(t).Seconds())
		}
	}
	if ls.Running {
		if st, serr := s.eng(app).StatsOnce(ctx, cid); serr == nil {
			ls.Stats = &st
		}
	}
	return ls
}

// serviceLiveStatus reports the live status of a cluster (service) app from the
// manager's view of its Swarm service: desired replicas vs running tasks. There
// is no single container to inspect, so status is derived from task convergence.
func (s *Service) serviceLiveStatus(ctx context.Context, app *models.Application, ls LiveStatus) LiveStatus {
	mgr, err := s.clients.For(0)
	if err != nil {
		return ls // manager unreachable — report the stored status
	}
	st, err := mgr.ServiceInspect(ctx, node.AppAlias(app))
	if err != nil {
		ls.Status = "no_container" // recorded as a service but the service is gone
		return ls
	}
	ls.HasContainer = true
	ls.ServiceReplicas = st.Replicas
	ls.ServiceRunningTasks = st.RunningTasks
	ls.Running = st.RunningTasks > 0
	// A service's desired replica count is the source of truth: 0 means stopped
	// (Stop scales to zero), otherwise Swarm is converging toward it. With desired
	// replicas > 0 but not all tasks up yet, the service is "starting" — never
	// "exited", since Swarm keeps (re)scheduling until the desired count is met.
	switch {
	case app.Status == models.AppStatusStopped || st.Replicas == 0:
		ls.Status = "stopped"
	case st.RunningTasks >= st.Replicas:
		ls.Status = "running"
	default:
		ls.Status = "starting"
	}
	// Uptime comes from the swarm control plane (the task's running-since timestamp),
	// not from inspecting a container — the task may sit on a node Miabi has no
	// Docker client for, where there is nothing to inspect. Without this a healthy
	// service showed no "running since" at all.
	ls.StartedAt = st.StartedAt
	if ls.Running && ls.StartedAt != "" {
		if t, perr := time.Parse(time.RFC3339Nano, ls.StartedAt); perr == nil {
			ls.UptimeSeconds = int64(time.Since(t).Seconds())
		}
	}
	// A resource snapshot from a running task's container. Stats have no
	// manager-side equivalent (there is no `docker service stats`), so this only
	// works when the task landed on a node Miabi has a client for; on an unmanaged
	// swarm member it is silently absent, and the rest of the status still holds.
	if ls.Running {
		if dc, cid, cerr := s.clients.ForServiceTask(ctx, node.AppAlias(app)); cerr == nil {
			if sample, serr := dc.StatsOnce(ctx, cid); serr == nil {
				ls.Stats = &sample
			}
		}
	}
	return ls
}

// deriveLiveStatus maps a container's Docker state + health into a headline
// status for the UI. intentionallyStopped distinguishes a user-stopped app from
// an unexpected exit (crash).
func deriveLiveStatus(state, health string, restarting, intentionallyStopped bool) string {
	switch state {
	case "restarting":
		return "restarting"
	case "paused":
		return "paused"
	case "created":
		return "created"
	case "exited", "dead":
		if intentionallyStopped {
			return "stopped"
		}
		return "exited"
	case "running":
		if restarting {
			return "restarting"
		}
		switch health {
		case "unhealthy":
			return "unhealthy"
		case "starting":
			return "starting"
		}
		return "running"
	}
	if restarting {
		return "restarting"
	}
	if state == "" {
		return "no_container"
	}
	return state
}

// emit records an app event (best-effort; recorder may be nil).
func (s *Service) emit(app *models.Application, t models.AppEventType, message string) {
	if s.events == nil {
		return
	}
	s.events.Record(&models.AppEvent{
		WorkspaceID: app.WorkspaceID, ApplicationID: app.ID,
		Type: t, Severity: models.SeverityInfo, Message: message,
	})
}

// normalizeImageTag keeps the Image field as a bare repository by splitting a
// tag embedded in the image reference into the Tag field. A tag in the image
// reference wins over a separately supplied tag; a digest-pinned ref is left
// intact (kept whole in Image, no tag).
func normalizeImageTag(image, tag string) (string, string) {
	img, embedded := models.SplitImageRef(strings.TrimSpace(image))
	if embedded != "" {
		return img, embedded
	}
	return img, strings.TrimSpace(tag)
}

func (s *Service) Create(workspaceID uint, in CreateInput) (*models.Application, error) {
	if s.quota.Enabled() {
		n, _ := s.apps.CountByWorkspace(workspaceID)
		if err := s.quota.CheckCreate(workspaceID, quota.ResourceApps, int(n)); err != nil {
			return nil, err
		}
		if err := s.quota.CheckComputeAdd(workspaceID, in.NanoCPUs, in.MemoryBytes, 0); err != nil {
			return nil, err
		}
	}
	if in.SourceType == "" {
		in.SourceType = models.AppSourceImage
	}
	// Keep Image as the bare repository: split any tag embedded in the image
	// reference (e.g. "nginx:1.2" from a compose import or a user typing the full
	// ref) into the Tag field.
	if in.SourceType == models.AppSourceImage {
		in.Image, in.Tag = normalizeImageTag(in.Image, in.Tag)
	}
	// A git app needs a clone URL, but a selected saved repository supplies one
	// (the deploy worker derives the URL from the repository when GitRepo is
	// empty), so only require an explicit URL when no repository is attached.
	if in.SourceType == models.AppSourceGit && strings.TrimSpace(in.GitRepo) == "" && in.GitRepositoryID == nil {
		return nil, ErrGitRepoRequired
	}
	if in.SourceType == models.AppSourceImage && strings.TrimSpace(in.Image) == "" {
		return nil, ErrImageRequired
	}
	if err := validateBuildConfig(in.SourceType, in.BuildMethod, in.Builder, in.Buildpacks, in.BuildEnv); err != nil {
		return nil, err
	}
	if err := s.customBuilderAllowed(workspaceID, in.Builder); err != nil {
		return nil, err
	}
	if err := s.validateStack(workspaceID, in.StackID); err != nil {
		return nil, err
	}
	if err := s.validateResources(in.MemoryBytes, in.NanoCPUs); err != nil {
		return nil, err
	}
	if err := validateGPUCount(in.GPUCount); err != nil {
		return nil, err
	}
	// GPU access is a hard-gated plan capability (device passthrough is
	// privileged): a workspace whose plan lacks AllowGPU cannot even save an app
	// that requests one. Re-checked at deploy as defense-in-depth.
	if err := s.gpuAllowed(workspaceID, in.GPUCount); err != nil {
		return nil, err
	}
	// Placement: default to the local node; validate the chosen node accepts new
	// placements (exists, not cordoned) and is reachable.
	serverID := in.ServerID
	if serverID == 0 {
		serverID = s.clients.LocalID()
	}
	if s.nodeGuard != nil {
		if err := s.nodeGuard.Placeable(serverID); err != nil {
			return nil, err
		}
	}
	if _, err := s.clients.For(serverID); err != nil {
		return nil, err
	}
	base := strings.TrimSpace(in.Handle)
	if base == "" {
		base = in.DisplayName
	}
	appName, err := s.uniqueName(workspaceID, base)
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = appName
	}
	app := &models.Application{
		WorkspaceID: workspaceID, Name: appName, DisplayName: displayName, SourceType: in.SourceType, ServerID: serverID,
		Icon:  in.Icon,
		Image: in.Image, Tag: in.Tag, GitRepo: in.GitRepo, GitRef: in.GitRef,
		BuildMethod: buildMethodForSource(in.SourceType, in.BuildMethod),
		Builder:     in.Builder, Buildpacks: in.Buildpacks, BuildEnv: in.BuildEnv,
		RegistryID: in.RegistryID, GitRepositoryID: in.GitRepositoryID, StackID: in.StackID,
		Command: in.Command, Port: in.Port,
		MemoryBytes: in.MemoryBytes, NanoCPUs: in.NanoCPUs,
		GPUCount: in.GPUCount, GPUKind: strings.TrimSpace(in.GPUKind),
		RestartPolicy: normalizeRestartPolicy(in.RestartPolicy),
		ImagePullPolicy:      normalizeImagePullPolicy(in.ImagePullPolicy),
		RuntimeKind:          in.RuntimeKind,
		Replicas:             in.Replicas,
		PlacementConstraints: in.PlacementConstraints,
		UpdateConfig:         in.UpdateConfig,
		Status:               models.AppStatusCreated,
		Metadata:             models.DefaultManagedBy(in.Metadata, models.ManagedByUser),
		Annotations:          in.Annotations,
		ContainerLabels:      docker.SanitizeUserLabels(in.ContainerLabels),
	}
	normalizeRuntime(app)
	// In cluster mode, default a caller-unspecified runtime to a replicated Swarm
	// service for interactive (user) creates. An explicit runtime_kind (including
	// "container") opts out, and declarative sources (Marketplace/Stack/GitOps) are
	// excluded so they stay deterministic. normalizeRuntime has already turned an
	// unspecified kind into "container", so branch on the original input.
	// The app can't hold a node-local volume yet (mounts attach after Create), so
	// mark the auto-choice: the first deploy re-checks it once storage is known and
	// downgrades a stateful app back to a container (see reconcileAutoRuntime).
	interactive := app.Metadata[models.MetaManagedBy] == models.ManagedByUser
	if defaultToServiceRuntime(in.RuntimeKind, s.clusterEnabled(), interactive) {
		app.RuntimeKind = models.RuntimeService
		app.Metadata = models.SetBuiltin(app.Metadata, models.MetaRuntimeAutoService, "true")
	}
	if err := s.validateRuntime(app); err != nil {
		// Surface the reason for an explicit choice; for the cluster-mode default,
		// degrade to a container rather than failing a create the caller didn't ask
		// to be a service (e.g. an app that can't run replicated).
		if in.RuntimeKind != "" {
			return nil, err
		}
		app.RuntimeKind = models.RuntimeContainer
		delete(app.Metadata, models.MetaRuntimeAutoService)
	}
	if err := s.apps.Create(app); err != nil {
		return nil, err
	}
	// Assign the stable alias/hostname now that the id exists; persisted so it
	// stays fixed across redeploys (it is the reverse-proxy upstream).
	app.Alias = node.NewAppAlias(slug.Token(8), app.ID)
	if err := s.apps.Update(app); err != nil {
		return nil, err
	}
	if err := s.SetNetworks(app, in.NetworkIDs); err != nil {
		return nil, err
	}
	// Back-compat: a single legacy port with no explicit list declares one port.
	specs := in.Ports
	if len(specs) == 0 && in.Port > 0 {
		specs = []PortSpec{{ContainerPort: in.Port, Protocol: "tcp"}}
	}
	if err := s.SetPorts(app, specs); err != nil {
		return nil, err
	}
	s.emit(app, models.EventAppCreated, "Application created")
	return app, nil
}

// SetNetworks attaches the given workspace networks to the app, always including the
// workspace's default network.
//
// The default is not optional garnish: it is the network the app shares with its
// databases, and in cluster mode it is the workspace's Swarm overlay — the thing that
// lets it reach a database on another node. An app that ends up on none deploys
// perfectly happily and then cannot resolve anything, with nothing to say why.
//
// So a missing default is repaired rather than tolerated. That path is reachable for a
// workspace that predates default networks, or one whose network was removed out of
// band — and it matters most for callers that never name a network at all, like GitOps,
// where the default is the only network the app was ever going to get.
func (s *Service) SetNetworks(app *models.Application, networkIDs []uint) error {
	all, err := s.networks.ListByWorkspace(app.WorkspaceID)
	if err != nil {
		return err
	}
	if !hasDefaultNetwork(all) && s.netEnsurer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		def, derr := s.netEnsurer.EnsureDefault(ctx, app.WorkspaceID)
		switch {
		case derr != nil:
			logger.Warn("could not ensure the workspace's default network; the app will have none",
				"workspace", app.WorkspaceID, "app", app.Name, "error", derr)
		case def != nil:
			all = append(all, *def)
		}
	}
	want := map[uint]bool{}
	for _, id := range networkIDs {
		want[id] = true
	}
	var selected []models.Network
	for i := range all {
		if want[all[i].ID] || all[i].IsDefault {
			selected = append(selected, all[i])
		}
	}
	return s.apps.ReplaceNetworks(app, selected)
}

func hasDefaultNetwork(nets []models.Network) bool {
	for i := range nets {
		if nets[i].IsDefault {
			return true
		}
	}
	return false
}

func (s *Service) Get(workspaceID, id uint) (*models.Application, error) {
	app, err := s.apps.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, err
	}
	s.annotateServer(app)
	// Detail read: surface where a cluster app's replicas actually run (live Swarm
	// placement), not just the static placement node. List views skip this.
	s.annotatePlacement(context.Background(), app)
	return app, nil
}

func (s *Service) List(workspaceID uint) ([]models.Application, error) {
	apps, err := s.apps.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range apps {
		s.annotateServer(&apps[i])
	}
	return apps, nil
}

// AppsReferencingSecret returns the workspace's apps whose env references the
// named secret (`${{ secrets.NAME }}`). Used by the Vault for "used by", the
// delete guard, and rotation fan-out. Satisfies secret.Consumers.
func (s *Service) AppsReferencingSecret(workspaceID uint, name string) ([]models.Application, error) {
	apps, err := s.apps.ListByWorkspaceWithEnv(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]models.Application, 0)
	for i := range apps {
		for _, ev := range apps[i].EnvVars {
			if secret.ReferencesSecret(ev.Value, name) {
				out = append(out, apps[i])
				break
			}
		}
	}
	return out, nil
}

func (s *Service) Update(app *models.Application) error {
	if app.SourceType == models.AppSourceImage {
		app.Image, app.Tag = normalizeImageTag(app.Image, app.Tag)
	}
	if err := s.validateStack(app.WorkspaceID, app.StackID); err != nil {
		return err
	}
	// A git app needs a clone URL or an attached repository to derive one from.
	if app.SourceType == models.AppSourceGit && strings.TrimSpace(app.GitRepo) == "" && app.GitRepositoryID == nil {
		return ErrGitRepoRequired
	}
	if err := validateBuildConfig(app.SourceType, app.BuildMethod, app.Builder, app.Buildpacks, app.BuildEnv); err != nil {
		return err
	}
	if err := s.customBuilderAllowed(app.WorkspaceID, app.Builder); err != nil {
		return err
	}
	app.BuildMethod = buildMethodForSource(app.SourceType, app.BuildMethod)
	if err := s.validateResources(app.MemoryBytes, app.NanoCPUs); err != nil {
		return err
	}
	// Aggregate workspace compute, excluding this app's current contribution.
	if err := s.quota.CheckComputeAdd(app.WorkspaceID, app.NanoCPUs, app.MemoryBytes, app.ID); err != nil {
		return err
	}
	if err := validateGPUCount(app.GPUCount); err != nil {
		return err
	}
	// Re-gate GPU on update so a workspace can't grant itself GPU access by editing
	// an app after a plan downgrade. GPU quota is enforced at deploy (running apps).
	app.GPUKind = strings.TrimSpace(app.GPUKind)
	if err := s.gpuAllowed(app.WorkspaceID, app.GPUCount); err != nil {
		return err
	}
	normalizeDeployConfig(app)
	normalizeHealthcheck(app)
	if err := s.validateRuntime(app); err != nil {
		return err
	}
	// Defense-in-depth: never let a reserved key reach the container via Update
	// (the interactive SetContainerLabels validates + gates; GitOps sets the field
	// directly and relies on this sanitize).
	app.ContainerLabels = docker.SanitizeUserLabels(app.ContainerLabels)
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventSettingsUpdated, "Settings updated")
	return nil
}

// normalizeDeployConfig clamps the deployment strategy and canary tuning to safe
// values so a bad payload can never produce a stuck or runaway rollout.
func normalizeDeployConfig(app *models.Application) {
	if !models.ValidDeployStrategy(app.DeployStrategy) {
		app.DeployStrategy = models.DeployRolling
	}
	app.RestartPolicy = normalizeRestartPolicy(app.RestartPolicy)
	app.ImagePullPolicy = normalizeImagePullPolicy(app.ImagePullPolicy)
	app.CanaryInitialWeight = clamp(app.CanaryInitialWeight, 1, 99)
	app.CanaryStepWeight = clamp(app.CanaryStepWeight, 1, 99)
	if app.CanaryStepIntervalSeconds < 10 {
		app.CanaryStepIntervalSeconds = 10
	}
	normalizeRuntime(app)
}

// defaultToServiceRuntime reports whether a caller-unspecified runtime should
// default to a replicated Swarm service: true only when the caller passed no
// explicit kind, cluster mode is on, AND the app was created interactively (by a
// user, not a declarative source). Any explicit choice (including "container")
// opts out, and declarative sources — Marketplace, Stack, GitOps — are excluded so
// they stay deterministic (a manifest/template must ask for a service on purpose,
// never "service iff cluster happened to be on at apply time").
func defaultToServiceRuntime(explicit models.RuntimeKind, clusterOn, interactive bool) bool {
	return explicit == "" && clusterOn && interactive
}

// normalizeRuntime defaults the runtime kind to container and clamps replicas to
// at least 1.
func normalizeRuntime(app *models.Application) {
	if !models.ValidRuntimeKind(app.RuntimeKind) {
		app.RuntimeKind = models.RuntimeContainer
	}
	if app.Replicas < 1 {
		app.Replicas = 1
	}
}

// validateRuntime rejects a service-runtime app when cluster mode is off, so a
// user can't create one that could never deploy, and guards against replicating
// an app backed by node-local storage (which would silently fork its data).
func (s *Service) validateRuntime(app *models.Application) error {
	if app.RuntimeKind == models.RuntimeService && !s.clusterEnabled() {
		return ErrClusterDisabled
	}
	if app.Replicas > MaxReplicas {
		return ErrTooManyReplicas
	}
	return s.requireSharedStorage(app, app.Replicas)
}

// requireSharedStorage guards a service app's storage against the ways a
// scheduled swarm task and node-local data diverge:
//   - a privileged host-path bind can never follow a task to another node, so it
//     is rejected for a service outright (any replica count);
//   - a node-local (rwo) managed volume gives each replica its own empty copy, so
//     it is rejected once replicas > 1 (a single replica is handled at deploy by
//     the runtime downgrade, or by an explicit node pin later).
//
// A no-op for container apps.
func (s *Service) requireSharedStorage(app *models.Application, replicas int) error {
	if app.RuntimeKind != models.RuntimeService {
		return nil
	}
	for _, m := range app.Mounts {
		if m.HostPreset != "" {
			return ErrHostBindService // node-local bind, can't move with the task
		}
		if m.VolumeID == 0 || replicas <= 1 {
			continue
		}
		v, err := s.volumes.FindInWorkspace(app.WorkspaceID, m.VolumeID)
		if err != nil {
			continue
		}
		if v.AccessMode != models.AccessRWX {
			return ErrLocalVolumeReplicated
		}
	}
	return nil
}

// hasNodeLocalStorage reports whether an app's mounts include storage that a
// rescheduled swarm task would leave behind: a privileged host-path bind, or an
// rwo (node-local) managed volume. The signal for whether an app is safe to run
// as a schedulable service.
func (s *Service) hasNodeLocalStorage(app *models.Application) bool {
	for _, m := range app.Mounts {
		if m.HostPreset != "" {
			return true // privileged host bind: node-local
		}
		if m.VolumeID == 0 {
			continue
		}
		if v, err := s.volumes.FindInWorkspace(app.WorkspaceID, m.VolumeID); err == nil && v.AccessMode != models.AccessRWX {
			return true // rwo managed volume: node-local
		}
	}
	return false
}

// reconcileAutoRuntime re-evaluates a cluster-mode auto-defaulted service at its
// first deploy, once the app's storage is known (mounts attach after create). If
// the app turns out to hold node-local state, it is downgraded to a node-pinned
// container so a rescheduled task can't leave the data behind. One-shot: the
// marker is cleared on the first deploy, so a later *explicit* runtime choice is
// always respected. A no-op for explicitly-chosen runtimes and already-deployed
// apps. Best-effort — a lookup/update failure just leaves the app as it was.
func (s *Service) reconcileAutoRuntime(appID uint) {
	app, err := s.apps.FindByID(appID)
	if err != nil || app.Metadata[models.MetaRuntimeAutoService] != "true" {
		return
	}
	// Only re-evaluate before the first release exists; after that the runtime is
	// settled and changing it mid-life would be a disruptive surprise.
	downgrade := app.CurrentReleaseID == nil && app.RuntimeKind == models.RuntimeService && s.hasNodeLocalStorage(app)
	if downgrade {
		app.RuntimeKind = models.RuntimeContainer
	}
	delete(app.Metadata, models.MetaRuntimeAutoService)
	if err := s.apps.Update(app); err != nil {
		return
	}
	if downgrade {
		s.emit(app, models.EventSettingsUpdated,
			"Runtime set to container: this app uses node-local storage, which a rescheduled cluster service task would leave behind. Attach a shared (rwx) volume to run it as a replicated service.")
	}
}

// normalizeRestartPolicy defaults an empty or unknown restart policy to
// unless-stopped (the platform's historical container behavior).
func normalizeRestartPolicy(p models.RestartPolicy) models.RestartPolicy {
	if !models.ValidRestartPolicy(p) {
		return models.RestartUnlessStopped
	}
	return p
}

// validateBuildConfig checks an app's build configuration against its source
// type: build knobs are only meaningful for git apps, and an explicit build
// method must be a known value. An empty method is allowed (defaults to auto).
func validateBuildConfig(sourceType models.AppSourceType, method models.AppBuildMethod, builder string, buildpacks []string, buildEnv map[string]string) error {
	if sourceType != models.AppSourceGit {
		// auto is the stored default (and "no build config"); only an explicit
		// non-auto method or any builder/buildpacks/env is rejected on image apps.
		explicit := method != "" && method != models.BuildAuto
		if explicit || builder != "" || len(buildpacks) > 0 || len(buildEnv) > 0 {
			return ErrBuildConfigOnImage
		}
		return nil
	}
	if method != "" && !models.ValidAppBuildMethod(method) {
		return ErrInvalidBuildMethod
	}
	return nil
}

// normalizeBuildMethod defaults an empty or unknown build method to auto.
func normalizeBuildMethod(m models.AppBuildMethod) models.AppBuildMethod {
	if !models.ValidAppBuildMethod(m) {
		return models.BuildAuto
	}
	return m
}

// buildMethodForSource returns the stored build method: a normalized method for
// git apps (defaulting to auto), and auto for image apps where it is unused.
func buildMethodForSource(sourceType models.AppSourceType, m models.AppBuildMethod) models.AppBuildMethod {
	if sourceType != models.AppSourceGit {
		return models.BuildAuto
	}
	return normalizeBuildMethod(m)
}

// normalizeImagePullPolicy defaults an empty or unknown image pull policy to
// always (the platform's historical behavior of pulling the tag each deploy).
func normalizeImagePullPolicy(p models.ImagePullPolicy) models.ImagePullPolicy {
	if !models.ValidImagePullPolicy(p) {
		return models.PullAlways
	}
	return p
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ErrNoActiveContainer is returned by Exec when the app has no running
// container to attach a shell to.
var ErrNoActiveContainer = errors.New("application has no active container")

// ErrTaskOnUnmanagedNode is the user-facing form of nodes.ErrTaskUnreachable: the
// app IS running, but on a swarm node with no Miabi agent, so there is no engine to
// open a shell or read processes through. Docker offers no manager-side equivalent
// of exec, so this is a hard limit, not a bug. Logs are unaffected — the manager
// aggregates those.
var ErrTaskOnUnmanagedNode = errors.New(
	"this app's task runs on a swarm node with no Miabi agent, so a shell cannot be opened. " +
		"Add the node to Miabi (install the agent) to use exec")

// EnsureExecAllowed returns a CapabilityDenied error when the workspace's plan
// does not permit opening an interactive shell into a container. Nil-safe on a
// disabled quota service (returns nil).
func (s *Service) EnsureExecAllowed(workspaceID uint) error {
	return s.quota.Require(workspaceID, quota.CapShellExec)
}

// Exec opens an interactive command stream inside the app's active container.
// Returns ErrNoActiveContainer when nothing is running. The caller owns the
// returned stream and must Close it.
func (s *Service) Exec(ctx context.Context, app *models.Application, opts docker.ExecOptions) (docker.ExecStream, error) {
	cid, dc, err := s.runtimeContainerID(ctx, app)
	if err != nil {
		return nil, ErrNoActiveContainer
	}
	return dc.Exec(ctx, cid, opts)
}

// Processes lists the running processes in the app's active container (the
// "docker top" view). Read-only — no shell capability needed. Returns
// ErrNoActiveContainer when nothing is running.
func (s *Service) Processes(ctx context.Context, app *models.Application, psArgs string) (docker.ProcessList, error) {
	cid, dc, err := s.runtimeContainerID(ctx, app)
	if err != nil {
		return docker.ProcessList{}, ErrNoActiveContainer
	}
	return dc.Top(ctx, cid, psArgs)
}

// activeContainerID returns the active release's container, or ErrNotDeployable
// when the app has never been deployed (or its container was removed).
func (s *Service) activeContainerID(appID uint) (string, error) {
	rel, err := s.releases.FindActive(appID)
	if err != nil || rel.ContainerID == "" {
		return "", ErrNotDeployable
	}
	return rel.ContainerID, nil
}

// runtimeContainerID resolves a container to inspect/exec/stream for the app and
// the Docker client that owns it. For a container app it's the active release's
// container on the app's node; for a cluster (service) app it's a running task of
// its Swarm service, on whichever node the scheduler placed it.
//
// Returns ErrTaskOnUnmanagedNode when a service is running but its node has no
// Miabi agent, and ErrNoActiveContainer when nothing is running at all.
func (s *Service) runtimeContainerID(ctx context.Context, app *models.Application) (string, docker.Client, error) {
	if app.RuntimeKind == models.RuntimeService {
		dc, cid, err := s.clients.ForServiceTask(ctx, node.AppAlias(app))
		switch {
		case err == nil:
			return cid, dc, nil
		case errors.Is(err, nodes.ErrTaskUnreachable):
			return "", nil, ErrTaskOnUnmanagedNode
		default:
			return "", nil, ErrNoActiveContainer
		}
	}
	cid, err := s.activeContainerID(app.ID)
	if err != nil {
		return "", nil, ErrNoActiveContainer
	}
	return cid, s.eng(app), nil
}

// Start starts the app's (stopped) active container. If a redeploy is required
// (config changed while stopped), it redeploys instead so the new config is
// applied rather than starting a stale container — returning that deployment so
// the caller can follow its logs. A plain start returns a nil deployment.
func (s *Service) Start(ctx context.Context, app *models.Application) (*models.Deployment, error) {
	if app.RedeployRequired {
		return s.Redeploy(app)
	}
	if app.RuntimeKind == models.RuntimeService {
		// Start = scale the service back to its desired replica count.
		replicas := uint64(app.Replicas)
		if replicas < 1 {
			replicas = 1
		}
		mgr, err := s.clients.For(0)
		if err != nil {
			return nil, err
		}
		if err := mgr.ServiceScale(ctx, node.AppAlias(app), replicas); err != nil {
			return nil, err
		}
		_ = s.apps.SetStatus(app.ID, models.AppStatusRunning)
		return nil, nil
	}
	cid, err := s.activeContainerID(app.ID)
	if err != nil {
		return nil, err
	}
	if err := s.eng(app).StartContainer(ctx, cid); err != nil {
		return nil, err
	}
	_ = s.apps.SetStatus(app.ID, models.AppStatusRunning)
	return nil, nil
}

// Stop stops the app's active container (it stays stopped; no auto-restart). For
// a service app it scales the service to zero replicas (the desired count stays
// recorded on the app, so Start restores it).
func (s *Service) Stop(ctx context.Context, app *models.Application) error {
	// Record the stop intent BEFORE stopping the container. Stopping is
	// asynchronous from the platform's view: the container's "die" event is picked
	// up by the events subscriber, which would flip a *running* app to "failed" on
	// a non-graceful exit code. Images built from a Git source typically run their
	// process as a child of /bin/sh (shell-form CMD), which doesn't forward
	// SIGTERM, so an intentional stop ends in a SIGKILL after the timeout (exit
	// 137) — indistinguishable from a crash by exit code alone. Persisting
	// "stopped" first makes the intent authoritative: nextStoredStatus only
	// touches a running app, so the die event no longer mislabels the stop as a
	// failure (and the live status reads "stopped", not "exited").
	prev := app.Status
	_ = s.apps.SetStatus(app.ID, models.AppStatusStopped)
	if app.RuntimeKind == models.RuntimeService {
		mgr, err := s.clients.For(0)
		if err != nil {
			_ = s.apps.SetStatus(app.ID, prev)
			return err
		}
		if err := mgr.ServiceScale(ctx, node.AppAlias(app), 0); err != nil {
			_ = s.apps.SetStatus(app.ID, prev)
			return err
		}
		return nil
	}
	cid, err := s.activeContainerID(app.ID)
	if err != nil {
		_ = s.apps.SetStatus(app.ID, prev)
		return err
	}
	if err := s.eng(app).StopContainer(ctx, cid, 10); err != nil {
		_ = s.apps.SetStatus(app.ID, prev)
		return err
	}
	return nil
}

// Restart restarts the app's active container. If a redeploy is required
// (config changed while stopped), it redeploys instead so the new config is
// applied rather than restarting a stale container — returning that deployment
// so the caller can follow its logs. A plain restart returns a nil deployment.
func (s *Service) Restart(ctx context.Context, app *models.Application) (*models.Deployment, error) {
	if app.RedeployRequired {
		return s.Redeploy(app)
	}
	if app.RuntimeKind == models.RuntimeService {
		// Restart = force a rolling restart of the service's tasks in place.
		mgr, err := s.clients.For(0)
		if err != nil {
			return nil, err
		}
		if err := mgr.ServiceRestart(ctx, node.AppAlias(app)); err != nil {
			return nil, err
		}
		_ = s.apps.SetStatus(app.ID, models.AppStatusRunning)
		return nil, nil
	}
	cid, err := s.activeContainerID(app.ID)
	if err != nil {
		return nil, err
	}
	if err := s.eng(app).RestartContainer(ctx, cid, 10); err != nil {
		return nil, err
	}
	_ = s.apps.SetStatus(app.ID, models.AppStatusRunning)
	return nil, nil
}

// Scale sets the replica count of a cluster (service) app and applies it to the
// live Swarm service immediately (no redeploy). Rejected for container apps or
// when cluster mode is off.
func (s *Service) Scale(ctx context.Context, app *models.Application, replicas int) error {
	if app.RuntimeKind != models.RuntimeService {
		return ErrNotService
	}
	if !s.clusterEnabled() {
		return ErrClusterDisabled
	}
	if replicas < 1 {
		replicas = 1
	}
	if replicas > MaxReplicas {
		return ErrTooManyReplicas
	}
	if err := s.requireSharedStorage(app, replicas); err != nil {
		return err
	}
	mgr, err := s.clients.For(0)
	if err != nil {
		return err
	}
	if err := mgr.ServiceScale(ctx, node.AppAlias(app), uint64(replicas)); err != nil {
		return err
	}
	app.Replicas = replicas
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventSettingsUpdated, fmt.Sprintf("Scaled to %d replica(s)", replicas))
	return nil
}

// WaitRunning polls the app's active container until it reports the running
// state, or returns an error on timeout. Used as a basic readiness gate for
// rolling restarts so the next app isn't restarted until this one recovers.
func (s *Service) WaitRunning(ctx context.Context, app *models.Application) error {
	cid, err := s.activeContainerID(app.ID)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if c, err := s.eng(app).InspectContainer(ctx, cid); err == nil && c.State == "running" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("container did not become ready within 30s")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Delete removes the application and its active container. Refuses while the
// app is running — it must be stopped first, to avoid yanking a live workload.
func (s *Service) Delete(ctx context.Context, app *models.Application) error {
	if s.LiveStatus(ctx, app).Running {
		return ErrAppRunning
	}
	// Record the deletion before the row is gone (emit reads app.ID/WorkspaceID
	// directly, no DB lookup) so the workspace event feed — and the live dashboard
	// — reflect it. Record publishes synchronously, so the live stream sees it
	// even though the app row is removed below.
	s.emit(app, models.EventAppDeleted, "Application deleted")
	if rel, err := s.releases.FindActive(app.ID); err == nil && rel.ContainerID != "" {
		_ = s.eng(app).StopContainer(ctx, rel.ContainerID, 10)
		_ = s.eng(app).RemoveContainer(ctx, rel.ContainerID, true)
	}
	// Remove the app's reverse-proxy routes (generated external-access + user) from
	// the database and the gateway, so none dangle against the deleted app.
	if s.routeSync != nil {
		_ = s.routeSync.RemoveAppRoutes(ctx, app.ID)
	}
	// Free the app's host-port bindings (managed auto-forward + user-requested) so
	// their host ports return to the per-node allocation pool.
	if s.portBindings != nil {
		_ = s.portBindings.DeleteByApp(app.ID)
	}
	return s.apps.Delete(app.ID)
}

// --- Env vars ---

func (s *Service) SetEnvVar(appID uint, key, value string, isSecret bool) error {
	stored := value
	if isSecret {
		app, err := s.apps.FindByID(appID)
		if err != nil {
			return err
		}
		enc, err := crypto.EncryptWS(app.WorkspaceID, value)
		if err != nil {
			return err
		}
		stored = enc
	}
	if err := s.apps.UpsertEnvVar(&models.AppEnvVar{ApplicationID: appID, Key: key, Value: stored, IsSecret: isSecret}); err != nil {
		return err
	}
	s.emitForApp(appID, models.EventEnvUpdated, "Environment variable set: "+key)
	return nil
}

// ImportEnvVars upserts every variable parsed from .env-style content (later
// duplicate keys win), encrypting them when isSecret. Returns the number set.
func (s *Service) ImportEnvVars(appID uint, content string, isSecret bool) (int, error) {
	var workspaceID uint
	if isSecret {
		app, err := s.apps.FindByID(appID)
		if err != nil {
			return 0, err
		}
		workspaceID = app.WorkspaceID
	}
	pairs := dotenv.Parse(content)
	for _, p := range pairs {
		stored := p.Value
		if isSecret {
			enc, err := crypto.EncryptWS(workspaceID, p.Value)
			if err != nil {
				return 0, err
			}
			stored = enc
		}
		if err := s.apps.UpsertEnvVar(&models.AppEnvVar{ApplicationID: appID, Key: p.Key, Value: stored, IsSecret: isSecret}); err != nil {
			return 0, err
		}
	}
	if len(pairs) > 0 {
		s.emitForApp(appID, models.EventEnvUpdated, fmt.Sprintf("Imported %d environment variables", len(pairs)))
	}
	return len(pairs), nil
}

func (s *Service) DeleteEnvVar(appID uint, key string) error {
	if err := s.apps.DeleteEnvVar(appID, key); err != nil {
		return err
	}
	s.emitForApp(appID, models.EventEnvUpdated, "Environment variable removed: "+key)
	return nil
}

// emitForApp loads the app (for its workspace) and records an event.
func (s *Service) emitForApp(appID uint, t models.AppEventType, message string) {
	if s.events == nil {
		return
	}
	app, err := s.apps.FindByID(appID)
	if err != nil {
		return
	}
	s.emit(app, t, message)
}

// --- Volume mounts ---

// AttachVolume mounts a workspace volume into the app at path. Takes effect on
// the next deploy.
func (s *Service) AttachVolume(app *models.Application, volumeID uint, path string) error {
	vol, err := s.volumes.FindInWorkspace(app.WorkspaceID, volumeID)
	if err != nil {
		return ErrVolumeNotFound
	}
	// A host-path volume binds an operator-managed path present on every node, so
	// it is node-agnostic (unlike a node-local Docker volume, which must co-locate
	// with the app). Other drivers must live on the app's node.
	if vol.Driver != models.VolumeDriverHost && vol.ServerID != app.ServerID {
		return ErrNodeMismatch
	}
	if path == "" {
		return ErrMountPathRequired
	}
	// A replicated service must not mount a node-local volume.
	if app.RuntimeKind == models.RuntimeService && app.Replicas > 1 && vol.AccessMode != models.AccessRWX {
		return ErrLocalVolumeReplicated
	}
	for i, m := range app.Mounts {
		if m.VolumeID == volumeID { // update path
			app.Mounts[i].Path = path
			return s.apps.Update(app)
		}
	}
	// Denormalize the host path for a host-path volume so the runtime binds it
	// without a volume lookup (the volume is immutable, so this can't drift).
	app.Mounts = append(app.Mounts, models.AppMount{VolumeID: vol.ID, DockerName: vol.DockerName, Path: path, HostPath: vol.HostPath})
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventVolumeAttached, "Volume attached at "+path)
	return nil
}

// DetachVolume removes a volume mount from the app.
func (s *Service) DetachVolume(app *models.Application, volumeID uint) error {
	out := app.Mounts[:0]
	for _, m := range app.Mounts {
		if m.VolumeID != volumeID {
			out = append(out, m)
		}
	}
	app.Mounts = out
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventVolumeDetached, "Volume detached")
	return nil
}

// AttachHostMount attaches an allow-listed privileged host bind (see package
// hostmount) to the app at path (preset default target when empty). Requires
// the app's workspace to be privileged; the host source path is resolved from
// the preset at deploy time and is never taken from the client. Takes effect on
// the next deploy.
func (s *Service) AttachHostMount(app *models.Application, preset, path string, readOnly bool) error {
	p, ok := hostmount.Get(preset)
	if !ok {
		return ErrUnknownHostPreset
	}
	if err := s.requirePrivileged(app.WorkspaceID); err != nil {
		return err
	}
	target := strings.TrimSpace(path)
	if target == "" {
		target = p.DefaultTarget
	}
	ro := readOnly && p.AllowReadOnly
	for i, m := range app.Mounts {
		if m.HostPreset == preset {
			app.Mounts[i].Path = target
			app.Mounts[i].ReadOnly = ro
			return s.apps.Update(app)
		}
	}
	app.Mounts = append(app.Mounts, models.AppMount{HostPreset: preset, Path: target, ReadOnly: ro})
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventVolumeAttached, "Host mount attached: "+p.Label+" at "+target)
	return nil
}

// DetachHostMount removes a privileged host bind from the app.
func (s *Service) DetachHostMount(app *models.Application, preset string) error {
	out := app.Mounts[:0]
	for _, m := range app.Mounts {
		if m.HostPreset != preset {
			out = append(out, m)
		}
	}
	app.Mounts = out
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventVolumeDetached, "Host mount detached")
	return nil
}

// requirePrivileged returns ErrHostMountNotPrivileged unless the workspace is
// flagged privileged (a platform-admin-only flag).
func (s *Service) requirePrivileged(workspaceID uint) error {
	if s.workspaces == nil {
		return ErrHostMountNotPrivileged
	}
	ws, err := s.workspaces.FindByID(workspaceID)
	if err != nil {
		return err
	}
	if !ws.Privileged {
		return ErrHostMountNotPrivileged
	}
	return nil
}

func (s *Service) ListEnvVars(appID uint) ([]models.AppEnvVar, error) {
	vars, err := s.apps.ListEnvVars(appID)
	if err != nil {
		return nil, err
	}
	for i := range vars {
		if vars[i].IsSecret {
			vars[i].Value = "••••••••" // mask secrets in responses
		}
	}
	return vars, nil
}

// RevealEnvVar returns the decrypted value of a single env var, scoped to the
// app (the caller resolves the app within the workspace). Secret values are
// decrypted; non-secret values are returned as-is. Used by the audited reveal
// endpoint so an operator can read a value they otherwise only see masked.
func (s *Service) RevealEnvVar(appID uint, key string) (string, error) {
	vars, err := s.apps.ListEnvVars(appID)
	if err != nil {
		return "", err
	}
	for _, v := range vars {
		if v.Key == key {
			if v.IsSecret {
				return crypto.Decrypt(v.Value)
			}
			return v.Value, nil
		}
	}
	return "", ErrEnvVarNotFound
}

// --- Deploy / rollback ---

// Deploy creates a deployment for the current app config and enqueues it.
// registryOverride, when non-nil, uses a different registry credential for this
// one deploy; otherwise the app's configured RegistryID is used. tagOverride,
// when non-empty, deploys a specific image tag (image source) for this deploy.
func (s *Service) Deploy(app *models.Application, registryOverride *uint, tagOverride string, strategy models.DeployStrategy) (*models.Deployment, error) {
	// Settle a cluster-mode auto-defaulted runtime now that storage is known: a
	// stateful app is pinned to a container before its first service is ever
	// created. Persisted here, so the worker (which reloads the app) sees the final
	// runtime. No-op for explicit runtimes and already-deployed apps.
	s.reconcileAutoRuntime(app.ID)
	regID := app.RegistryID
	if registryOverride != nil {
		regID = registryOverride
	}
	// For image apps, persist the composed image:tag onto the deployment so the
	// release records exactly what was deployed; git apps build their own image.
	image := ""
	if app.SourceType != models.AppSourceGit {
		image = app.ImageRef(tagOverride)
	}
	return s.enqueue(app.ID, app.ServerID, image, "manual", regID, s.resolveStrategy(app, strategy))
}

// resolveStrategy picks the effective rollout method: an explicit choice, else
// the app's default. Canary needs a running release to shift traffic against, so
// a canary on a never-deployed app falls back to rolling.
func (s *Service) resolveStrategy(app *models.Application, requested models.DeployStrategy) models.DeployStrategy {
	st := requested
	if !models.ValidDeployStrategy(st) {
		st = app.DeployStrategy
	}
	if !models.ValidDeployStrategy(st) {
		st = models.DeployRolling
	}
	if st == models.DeployCanary && app.CurrentReleaseID == nil {
		st = models.DeployRolling
	}
	return st
}

// Redeploy enqueues a deploy that applies the app's current configuration.
// Used by Start/Restart when a redeploy is required.
func (s *Service) Redeploy(app *models.Application) (*models.Deployment, error) {
	s.reconcileAutoRuntime(app.ID)
	image := ""
	if app.SourceType != models.AppSourceGit {
		image = app.ImageRef("")
	}
	return s.enqueue(app.ID, app.ServerID, image, "auto", app.RegistryID, models.DeployRolling)
}

// EnsurePublished reconciles the host ports an app's running container publishes
// with its approved bindings, enqueuing a rolling redeploy when they differ —
// either to open a newly-added port (Docker can't add one to a running
// container) or to drop one whose binding was released (e.g. its route was
// deleted). Idempotent and best-effort: a no-op when the live set already
// matches, the app isn't running, or the node is offline. Satisfies the route
// service's PortPublisher.
func (s *Service) EnsurePublished(ctx context.Context, appID uint) error {
	if s.portBindings == nil {
		return nil
	}
	app, err := s.apps.FindByID(appID)
	if err != nil {
		return err
	}
	if app.CurrentReleaseID == nil {
		return nil // not running yet — the first deploy publishes everything
	}
	rel, err := s.releases.FindActive(appID)
	if err != nil || rel.ContainerID == "" {
		return nil
	}
	eng, err := s.clients.For(app.ServerID)
	if err != nil {
		return nil // node offline — republishes on its next deploy
	}
	cfg, err := eng.InspectContainerConfig(ctx, rel.ContainerID)
	if err != nil {
		return nil
	}
	approved, err := s.portBindings.ListApprovedByApp(appID)
	if err != nil {
		return err
	}
	want := map[int]bool{}
	for _, b := range approved {
		want[b.HostPort] = true
	}
	live := map[int]bool{}
	for _, p := range cfg.Ports {
		if p.HostPort != 0 {
			live[p.HostPort] = true
		}
	}
	if !samePortSet(want, live) {
		// A binding was added or removed since the last deploy: recreate so the
		// container publishes exactly the approved set. Rolling, so no downtime;
		// swapAndRelease re-syncs the route on success.
		_, derr := s.Redeploy(app)
		return derr
	}
	return nil
}

// samePortSet reports whether two host-port sets are equal.
func samePortSet(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for p := range a {
		if !b[p] {
			return false
		}
	}
	return true
}

func (s *Service) AutoRedeploy(app *models.Application) (*models.Deployment, error) {
	if app.CurrentReleaseID == nil {
		return nil, nil
	}
	if app.Status == models.AppStatusStopped {
		return nil, s.apps.SetRedeployRequired(app.ID, true)
	}
	return s.Redeploy(app)
}

func (s *Service) MarkRedeployRequired(app *models.Application) (bool, error) {
	if app.CurrentReleaseID == nil {
		return false, nil
	}
	if err := s.apps.SetRedeployRequired(app.ID, true); err != nil {
		return false, err
	}
	app.RedeployRequired = true
	return true, nil
}

// --- Custom container labels (Traefik &c.) ---

// Limits and coded errors for user-defined container labels. The reserved-prefix
// protection lives in the docker package (docker.SanitizeUserLabels).
const (
	maxContainerLabels  = 50
	maxLabelKeyLength   = 256
	maxLabelValueLength = 4096
)

// labelError carries a stable machine code so the API envelope surfaces it
// (the error_handlers read Code() off the returned error).
type labelError struct {
	code string
	msg  string
}

func (e *labelError) Error() string { return e.msg }
func (e *labelError) Code() string  { return e.code }

var (
	// ErrCustomLabelsDisabled is returned when the fleet-wide kill-switch is off.
	ErrCustomLabelsDisabled = &labelError{code: "FEATURE_DISABLED", msg: "custom container labels are disabled by the administrator"}
	// ErrLabelReserved is returned when a user tries to set a platform-reserved key.
	ErrLabelReserved = &labelError{code: "LABEL_RESERVED", msg: "label key is reserved by the platform (io.miabi.*, com.docker.*)"}
	// ErrTooManyLabels / ErrLabelInvalid guard size and shape.
	ErrTooManyLabels = &labelError{code: "LABELS_TOO_MANY", msg: fmt.Sprintf("too many container labels (max %d)", maxContainerLabels)}
	ErrLabelInvalid  = &labelError{code: "LABEL_INVALID", msg: "label key must be non-empty and within length limits"}
)

// customBuilderAllowed gates a per-app custom buildpack builder image behind the
// workspace's plan capability. A custom builder runs on the runner with docker
// access (pack --trust-builder --docker-host inherit), so on shared/multi-tenant
// runners it is an arbitrary-code vector; the platform default is used otherwise.
// A blank builder always passes (it means "use the platform default").
func (s *Service) customBuilderAllowed(workspaceID uint, builder string) error {
	if strings.TrimSpace(builder) == "" || s.quota == nil {
		return nil
	}
	return s.quota.Require(workspaceID, quota.CapCustomBuilder)
}

// validateGPUCount bounds the requested GPU units. 0 = none; the upper bound is
// a sanity cap (no single node exposes near this many whole cards).
func validateGPUCount(gpuCount int) error {
	if gpuCount < 0 || gpuCount > 64 {
		return ErrInvalidGPUCount
	}
	return nil
}

// gpuAllowed gates a GPU request behind the workspace's AllowGPU plan capability.
// A request of 0 (no GPU) always passes; nil quota (single-tenant) skips the gate.
func (s *Service) gpuAllowed(workspaceID uint, gpuCount int) error {
	if gpuCount <= 0 || s.quota == nil {
		return nil
	}
	return s.quota.Require(workspaceID, quota.CapGPU)
}

// customLabelsAllowed resolves the two-layer admin gate: the global kill-switch
// (settings) AND the per-workspace plan capability. Returns a coded error when
// the feature is not permitted for the workspace.
func (s *Service) customLabelsAllowed(workspaceID uint) error {
	if s.settings != nil && !s.settings.Bool(settings.KeyCustomLabelsEnabled, true) {
		return ErrCustomLabelsDisabled
	}
	if s.quota != nil {
		return s.quota.Require(workspaceID, quota.CapCustomLabels)
	}
	return nil
}

// validateContainerLabels rejects reserved keys explicitly (so an interactive
// edit gets a clear error rather than a silent strip), enforces the count/length
// caps, and trims keys. Returns the cleaned map (nil when empty).
func validateContainerLabels(in map[string]string) (map[string]string, error) {
	if len(in) > maxContainerLabels {
		return nil, ErrTooManyLabels
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" || len(k) > maxLabelKeyLength || len(v) > maxLabelValueLength {
			return nil, ErrLabelInvalid
		}
		if docker.IsReservedLabelKey(k) {
			return nil, ErrLabelReserved
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// SetContainerLabels validates and persists an app's user-defined Docker labels,
// enforcing the admin gate (§ plan capability + global kill-switch) and the
// reserved-prefix protection. Labels take effect on the next deploy (the caller
// marks the app redeploy-required, exactly like ports/volumes/env edits).
func (s *Service) SetContainerLabels(app *models.Application, labels map[string]string) error {
	if err := s.customLabelsAllowed(app.WorkspaceID); err != nil {
		return err
	}
	clean, err := validateContainerLabels(labels)
	if err != nil {
		return err
	}
	if err := s.apps.SetContainerLabels(app.ID, clean); err != nil {
		return err
	}
	app.ContainerLabels = clean
	return nil
}

// Rollback re-deploys a previous release's image.
func (s *Service) Rollback(app *models.Application, releaseID uint) (*models.Deployment, error) {
	rel, err := s.releases.FindByID(releaseID)
	if err != nil || rel.ApplicationID != app.ID {
		return nil, fmt.Errorf("release not found")
	}
	return s.enqueue(app.ID, app.ServerID, rel.Image, "rollback", app.RegistryID, models.DeployRolling)
}

// Canary deployment

var (
	ErrNoCanary     = errors.New("no canary deployment in progress")
	ErrCanaryActive = errors.New("a canary deployment is already in progress")
)

func (s *Service) StartCanary(app *models.Application, registryOverride *uint, tagOverride string) (*models.Deployment, error) {
	if app.CurrentReleaseID == nil {
		return nil, ErrNotDeployable
	}
	if app.CanaryReleaseID != nil {
		return nil, ErrCanaryActive
	}
	return s.Deploy(app, registryOverride, tagOverride, models.DeployCanary)
}

// SetCanaryWeight changes the share of traffic sent to the canary (1–99) and
// re-syncs the route immediately — no redeploy, no downtime.
func (s *Service) SetCanaryWeight(ctx context.Context, app *models.Application, weight int) error {
	if app.CanaryReleaseID == nil {
		return ErrNoCanary
	}
	if weight < 1 {
		weight = 1
	}
	if weight > 99 {
		weight = 99
	}
	if err := s.apps.SetCanary(app.ID, app.CanaryReleaseID, weight); err != nil {
		return err
	}
	if s.routeSync != nil {
		_ = s.routeSync.SyncRoute(ctx, app.ID)
	}
	s.emit(app, models.EventDeployStarted, fmt.Sprintf("Canary traffic set to %d%%", weight))
	return nil
}

// PromoteCanary makes the canary the new stable release. It enqueues a normal
// deploy of the canary's image; the deploy pipeline retires the old stable and
// the in-progress canary, leaving a single full-traffic release.
func (s *Service) PromoteCanary(app *models.Application) (*models.Deployment, error) {
	if app.CanaryReleaseID == nil {
		return nil, ErrNoCanary
	}
	rel, err := s.releases.FindByID(*app.CanaryReleaseID)
	if err != nil {
		return nil, ErrReleaseNotFound
	}
	image := ""
	if app.SourceType != models.AppSourceGit {
		image = rel.Image
	}
	s.emit(app, models.EventDeployStarted, "Promoting canary to stable")
	return s.enqueue(app.ID, app.ServerID, image, "manual", app.RegistryID, models.DeployRolling)
}

// AbortCanary stops the canary container, discards its release, and returns all
// traffic to the stable release.
func (s *Service) AbortCanary(ctx context.Context, app *models.Application) error {
	if app.CanaryReleaseID == nil {
		return ErrNoCanary
	}
	if rel, err := s.releases.FindByID(*app.CanaryReleaseID); err == nil {
		if rel.ContainerID != "" {
			_ = s.eng(app).StopContainer(ctx, rel.ContainerID, 10)
			_ = s.eng(app).RemoveContainer(ctx, rel.ContainerID, true)
		}
		// Close out the canary rollout's (non-terminal) deployment.
		s.finalizeCanaryDeployment(rel.DeploymentID, "canary aborted by user")
		_ = s.releases.Delete(rel.ID)
	}
	if err := s.apps.SetCanary(app.ID, nil, 0); err != nil {
		return err
	}
	// Returning to the stable release: the app is running, not stuck "deploying".
	_ = s.apps.SetStatus(app.ID, models.AppStatusRunning)
	if s.routeSync != nil {
		_ = s.routeSync.SyncRoute(ctx, app.ID)
	}
	s.emit(app, models.EventDeployStarted, "Canary aborted; all traffic on stable")
	return nil
}

// finalizeCanaryDeployment closes out a non-terminal canary rollout deployment
// when the canary is aborted manually (the worker handles promote/auto paths).
// Best-effort: appends a final log line and marks the deployment failed.
func (s *Service) finalizeCanaryDeployment(deploymentID uint, line string) {
	if deploymentID == 0 {
		return
	}
	dep, err := s.deployments.FindByID(deploymentID)
	if err != nil || dep.Status.IsTerminal() {
		return
	}
	_ = s.deployments.AppendLog(deploymentID, line)
	finished := time.Now()
	dep.Status = models.DeploymentFailed
	dep.FinishedAt = &finished
	_ = s.deployments.Update(dep)
}

func (s *Service) enqueue(appID, serverID uint, image, trigger string, registryID *uint, strategy models.DeployStrategy) (*models.Deployment, error) {
	if !models.ValidDeployStrategy(strategy) {
		strategy = models.DeployRolling
	}
	dep := &models.Deployment{ApplicationID: appID, Image: image, Trigger: trigger, Strategy: strategy, RegistryID: registryID, Status: models.DeploymentPending}
	if err := s.deployments.Create(dep); err != nil {
		return nil, err
	}
	if err := s.producer.EnqueueDeploy(dep.ID, serverID); err != nil {
		return nil, err
	}
	return dep, nil
}

func (s *Service) ListDeployments(appID uint, limit int) ([]models.Deployment, error) {
	deps, err := s.deployments.ListByApp(appID, limit)
	if err != nil {
		return nil, err
	}
	// Flag the deployment that produced the active release as the live one.
	if rel, err := s.releases.FindActive(appID); err == nil {
		for i := range deps {
			if deps[i].ID == rel.DeploymentID {
				deps[i].Current = true
			}
		}
	}
	return deps, nil
}

func (s *Service) GetDeployment(id uint) (*models.Deployment, error) {
	return s.deployments.FindByID(id)
}

func (s *Service) ListReleases(appID uint) ([]models.Release, error) {
	return s.releases.ListByApp(appID)
}

// GetRelease loads a release and verifies it belongs to the application.
func (s *Service) GetRelease(app *models.Application, releaseID uint) (*models.Release, error) {
	rel, err := s.releases.FindByID(releaseID)
	if err != nil || rel.ApplicationID != app.ID {
		return nil, ErrReleaseNotFound
	}
	return rel, nil
}

// SetReleasePinned protects (or unprotects) a release from cleanup deletion.
func (s *Service) SetReleasePinned(app *models.Application, releaseID uint, pinned bool) (*models.Release, error) {
	rel, err := s.GetRelease(app, releaseID)
	if err != nil {
		return nil, err
	}
	if err := s.releases.SetPinned(rel.ID, pinned); err != nil {
		return nil, err
	}
	rel.Pinned = pinned
	return rel, nil
}

// DeleteRelease removes a non-active, non-pinned release and its retired
// container (best-effort).
func (s *Service) DeleteRelease(ctx context.Context, app *models.Application, releaseID uint) error {
	rel, err := s.GetRelease(app, releaseID)
	if err != nil {
		return err
	}
	if rel.Active {
		return ErrReleaseActive
	}
	if rel.Pinned {
		return ErrReleasePinned
	}
	if rel.ContainerID != "" {
		_ = s.eng(app).RemoveContainer(ctx, rel.ContainerID, true)
	}
	return s.releases.Delete(rel.ID)
}

// uniqueName derives a workspace-unique slug handle from base, suffixing on
// collision. Mirrors the workspace naming convention via the shared slug package.
func (s *Service) uniqueName(workspaceID uint, base string) (string, error) {
	return slug.Unique(base, "app", func(candidate string) (bool, error) {
		return s.apps.ExistsByName(workspaceID, candidate)
	})
}

// SetName validates and applies a new handle to app in memory (the caller
// persists via Update). The value is normalized to canonical slug form and must
// be non-empty and unique within the workspace. Unlike create it does not
// auto-suffix, so a rename onto a taken handle is an error. A no-op when
// unchanged. Mirrors workspace.SetName.
func (s *Service) SetName(app *models.Application, newName string) error {
	name := slug.Make(newName, "")
	if name == "" {
		return ErrNameInvalid
	}
	if name == app.Name {
		return nil
	}
	exists, err := s.apps.ExistsByName(app.WorkspaceID, name)
	if err != nil {
		return err
	}
	if exists {
		return ErrSlugTaken
	}
	app.Name = name
	return nil
}

// IDByUID resolves an application's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.apps.IDByUID(uid) }
