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

// Package application manages applications, their config, and deployments.
package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/datavolume"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/dotenv"
	"github.com/miabi-io/miabi/internal/hostmount"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	configsvc "github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/events"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/worker"
	"gorm.io/gorm"
)

var (
	ErrImageRequired = errors.New("image is required for image-source applications")
	// Pulling it would use the platform credential, which every namespace accepts.
	ErrImageNotPermitted = errors.New("image is not available to this workspace")
	ErrGitRepoRequired   = errors.New("git_repo is required for git-source applications")
	// ErrPipelinesUnavailable means the pipeline service is not wired (tests, or a build without it).
	ErrPipelinesUnavailable = errors.New("pipelines are not available on this platform")
	// ErrRepoPipelinesDisabled is the operator kill-switch for adopting pipelines out of repositories.
	ErrRepoPipelinesDisabled = errors.New("repository pipelines are disabled on this platform")
	// ErrNotGitApp means the operation only applies to an application that builds from git.
	ErrNotGitApp          = errors.New("application does not build from a git repository")
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
	ErrVolumeLocation     = errors.New("the volume is in a different location than this application")
	ErrStackLocation      = errors.New("the stack is in a different location than this application")
	ErrConfigNotFound     = errors.New("config not found in workspace")
	ErrConfigKeyNotFound  = errors.New("the config does not contain that file")
	ErrMountNotFound      = errors.New("that mount is not attached to this application")

	ErrUnknownHostPreset      = errors.New("unknown host mount preset")
	ErrHostMountNotPrivileged = errors.New("host mounts require a privileged workspace")

	ErrInvalidGPUCount = errors.New("gpu_count must be between 0 and 64")

	ErrClusterDisabled       = errors.New("cluster mode is not enabled; cannot run this application as a service")
	ErrNotService            = errors.New("this operation is only valid for service-runtime (cluster) applications")
	ErrLocalVolumeReplicated = errors.New("a replicated (replicas>1) service cannot mount a node-local volume; use a shared (nfs/cifs) volume or set replicas to 1")
	ErrHostBindService       = errors.New("a service-runtime app cannot use a privileged host mount; host paths are node-local and don't follow a rescheduled task — use the container runtime or a managed volume")
	// A guard that cannot read a mounted volume cannot prove it shared; guessing forks the data.
	ErrVolumeUnverifiable = errors.New("a mounted volume could not be read, so it cannot be confirmed as shared storage; replication is refused until it resolves")
	ErrTooManyReplicas    = fmt.Errorf("replica count exceeds the maximum of %d", MaxReplicas)
	ErrPortRange          = errors.New("container port must be between 1 and 65535")
)

// MaxReplicas caps a service app's replica count.
const MaxReplicas = models.MaxServiceReplicas

// CreateInput describes a new application.
type CreateInput struct {
	// Handle is derived from DisplayName when blank.
	DisplayName string
	Handle      string
	ServerID    uint // node to place on (0 = local)
	SourceType  models.AppSourceType
	Icon        string // optional logo (URL or "mdi-…" class); set from a template
	Image       string
	Tag         string
	GitRepo     string
	GitRef      string
	// Git source only; rejected for image apps.
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
	// Gated by the AllowGPU plan capability; 0 = none.
	GPUCount int
	GPUKind  string
	// Empty keeps the image's user; validated against the workspace security profile.
	RunAsUser string
	// Both are allow-listed and gated on the workspace.
	AddCapabilities        []string
	Devices                []string
	ReadOnlyRootFilesystem bool
	NoNewPrivileges        bool
	DropCapabilities       []string
	// Empty leaves the model default (rolling).
	DeployStrategy models.DeployStrategy
	// Healthcheck is the container health probe; nil leaves none. Zero timing fields take the defaults.
	Healthcheck     *Healthcheck
	RestartPolicy   models.RestartPolicy
	ImagePullPolicy models.ImagePullPolicy
	// Service runtime is rejected when cluster mode is off.
	RuntimeKind          models.RuntimeKind
	Replicas             int
	PlacementConstraints []string
	UpdateConfig         *models.ServiceUpdateConfig
	// HTTP handlers must sanitize user input before setting reserved "miabi.io/" keys.
	Metadata    models.Metadata
	Annotations models.Metadata
	// Reserved keys (io.miabi.*, com.docker.*) are sanitized on create regardless of caller.
	ContainerLabels map[string]string
	// A repository that turns out to carry no pipeline is not an error; the app builds directly.
	UsePipeline bool
	// nil for non-interactive callers (GitOps, marketplace).
	UserID *uint
}

type Service struct {
	// grantsEnabled is MIABI_CONTAINER_GRANTS_ENABLED; false until an operator opts in.
	grantsEnabled bool
	apps          *repositories.ApplicationRepository
	deployments   *repositories.DeploymentRepository
	releases      *repositories.ReleaseRepository
	volumes       volumeLookup
	routes        *repositories.RouteRepository
	networks      *repositories.NetworkRepository
	stacks        *repositories.StackRepository
	ports         *repositories.AppPortRepository
	appEvents     *repositories.AppEventRepository
	clients       NodeDocker
	producer      *worker.Producer
	events        events.Recorder
	routeSync     RouteSyncer
	settings      *settings.Provider
	nodeGuard     NodeGuard
	serverInfo    ServerInfo
	nodeNamer     NodeNamer
	gitopsNamer   GitOpsNamer
	workspaces    WorkspaceInfo
	portBindings  *repositories.PortBindingRepository
	quota         *quota.Service
	cluster       ClusterCap
	netEnsurer    NetworkEnsurer
	pipelines     Pipelines
	imageGuard    ImageGuard
	configs       ConfigReader
	stackEnv      StackEnvReader
}

// ConfigReader reads a mounted config's files, for the secret-reference scan.
type ConfigReader interface {
	Get(workspaceID, id uint) (*models.Config, error)
	Data(cfg *models.Config) (map[string]string, error)
}

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// Pipelines is an interface so this package doesn't depend on the pipeline service's git machinery.
type Pipelines interface {
	// AdoptForApp returns pipeline.ErrNoPipelineInRepo when the repository carries none.
	AdoptForApp(ctx context.Context, app *models.Application, userID *uint) (*models.PipelineDefinition, error)
	// RepoPipelineForApp returns the app's enabled repo-owned pipeline, or nil.
	RepoPipelineForApp(appID uint) (*models.PipelineDefinition, error)
	// TriggerForApp starts a run of that pipeline.
	TriggerForApp(app *models.Application, trigger string, userID *uint, noCache bool) (*models.PipelineRun, error)
	// DeleteForApp drops the app's repo-owned pipelines and unbinds the rest.
	DeleteForApp(workspaceID, appID uint) error
	// GetRun loads a pipeline run of the workspace.
	GetRun(workspaceID, id uint) (*models.PipelineRun, error)
	// SyncFromRepo reports whether the stored spec changed; an empty ref uses the tracked one.
	SyncFromRepo(ctx context.Context, p *models.PipelineDefinition, ref string) (bool, error)
}

func (s *Service) SetPipelines(p Pipelines) { s.pipelines = p }

// NetworkEnsurer creates a missing default network so every app can join it.
type NetworkEnsurer interface {
	EnsureDefault(ctx context.Context, workspaceID uint) (*models.Network, error)
}

// Without it, a missing default network cannot be created.
func (s *Service) SetNetworkEnsurer(e NetworkEnsurer) { s.netEnsurer = e }

type ClusterCap interface {
	IsSwarm(clusterID uint) bool
	Manager(ctx context.Context, clusterID uint) (docker.Client, error)
	ClusterOfServer(serverID uint) uint
}

// SetClusterCap with nil treats cluster mode as off, rejecting service-runtime apps.
func (s *Service) SetClusterCap(c ClusterCap) { s.cluster = c }

// isSwarm reports whether the app's cluster runs the swarm a service app needs.
func (s *Service) isSwarm(clusterID uint) bool {
	return s.cluster != nil && s.cluster.IsSwarm(clusterID)
}

// ClusterOfServer is the cluster a node belongs to, resolved even for the local node.
func (s *Service) ClusterOfServer(serverID uint) uint { return s.clusterOf(serverID) }

func (s *Service) clusterOf(serverID uint) uint {
	if s.cluster == nil {
		return models.DefaultClusterID
	}
	return s.cluster.ClusterOfServer(serverID)
}

func (s *Service) swarmManager(ctx context.Context, app *models.Application) (docker.Client, error) {
	if s.cluster == nil {
		return s.clients.For(0)
	}
	return s.cluster.Manager(ctx, app.ClusterID)
}

// SetPortBindings wires the port-binding repository, whose rows go with a deleted app.
func (s *Service) SetPortBindings(r *repositories.PortBindingRepository) { s.portBindings = r }

// ExternalLabelTaken checks across all workspaces since the label maps to a platform-wide host.
func (s *Service) ExternalLabelTaken(label string, exceptAppID uint) (bool, error) {
	return s.apps.ExternalLabelTaken(label, exceptAppID)
}

type WorkspaceInfo interface {
	FindByID(id uint) (*models.Workspace, error)
}

// SetWorkspaceInfo wires the resolver used to check a workspace's privileged flag.
func (s *Service) SetWorkspaceInfo(w WorkspaceInfo) { s.workspaces = w }

// ImageGuard only constrains built-in registry refs, where a pull uses the platform credential.
type ImageGuard interface {
	ValidateImageRef(workspaceID uint, ref string) error
}

// SetImageGuard wires the internal-registry image check (nil skips it).
func (s *Service) SetImageGuard(g ImageGuard) { s.imageGuard = g }

func (s *Service) SetConfigs(c ConfigReader) { s.configs = c }

// StackEnvReader lists a stack's shared env, for the secret-reference scan.
type StackEnvReader interface {
	ListByStack(stackID uint) ([]models.StackEnvVar, error)
}

func (s *Service) SetStackEnv(r StackEnvReader) { s.stackEnv = r }

func (s *Service) checkImage(workspaceID uint, image, tag string) error {
	if s.imageGuard == nil || strings.TrimSpace(image) == "" {
		return nil
	}
	if err := s.imageGuard.ValidateImageRef(workspaceID, models.ComposeImageRef(image, tag)); err != nil {
		return fmt.Errorf("%w: %w", ErrImageNotPermitted, err)
	}
	return nil
}

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
	// Only the node running a service task can see its container.
	ForServiceTask(ctx context.Context, clusterID uint, serviceName string) (docker.Client, string, error)
}

type NodeGuard interface {
	Placeable(serverID uint) error
}

// Optional: read paths degrade to an empty server name when unset.
type ServerInfo interface {
	Get(id uint) (*models.Server, error)
}

// SetNodeGuard wires the placement guard consulted when creating an app on a node.
func (s *Service) SetNodeGuard(g NodeGuard) { s.nodeGuard = g }

// SetServerInfo wires the resolver used to annotate apps with their node's name.
func (s *Service) SetServerInfo(si ServerInfo) { s.serverInfo = si }

// Optional: placement degrades to a short swarm id when unset.
type NodeNamer interface {
	NameBySwarmNodeID(swarmNodeID string) string
}

func (s *Service) SetNodeNamer(n NodeNamer) { s.nodeNamer = n }

// GitOpsNamer resolves a GitOps project id to its name.
type GitOpsNamer interface {
	NameInWorkspace(workspaceID, id uint) (string, error)
}

// SetGitOpsNamer wires the project lookup that names a GitOps-managed app's project.
func (s *Service) SetGitOpsNamer(n GitOpsNamer) { s.gitopsNamer = n }

func (s *Service) annotateGitOps(app *models.Application) {
	if s.gitopsNamer == nil || app.Metadata[models.MetaManagedBy] != models.ManagedByGitOps {
		return
	}
	id, err := strconv.ParseUint(app.Metadata[models.MetaGitOpsSource], 10, 64)
	if err != nil {
		return
	}
	if name, err := s.gitopsNamer.NameInWorkspace(app.WorkspaceID, uint(id)); err == nil {
		app.GitOpsSourceName = name
	}
}

func (s *Service) annotateServer(app *models.Application) {
	if app == nil || s.serverInfo == nil {
		return
	}
	if srv, err := s.serverInfo.Get(app.ServerID); err == nil && srv != nil {
		app.ServerName = srv.Label()
	}
}

// Only called on the app-detail read, never per app in a list.
func (s *Service) annotatePlacement(ctx context.Context, app *models.Application) {
	if app == nil || app.RuntimeKind != models.RuntimeService {
		return
	}
	if !s.isSwarm(app.ClusterID) {
		return
	}
	mgr, err := s.swarmManager(ctx, app)
	if err != nil {
		return
	}
	st, err := mgr.ServiceInspect(ctx, node.AppAlias(app))
	if err != nil {
		return
	}
	running, desired := st.RunningTasks, st.Replicas
	app.RunningReplicas, app.DesiredReplicas = &running, &desired
	if len(st.Placement) == 0 {
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

func shortSwarmID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Returns an offline client when the agent is disconnected: critical ops fail, cleanups no-op.
func (s *Service) eng(app *models.Application) docker.Client {
	dc, err := s.clients.For(app.ServerID)
	if err != nil {
		return docker.Offline(err)
	}
	return dc
}

// RouteSyncer is injected after construction to avoid an import cycle.
type RouteSyncer interface {
	SyncRoute(ctx context.Context, appID uint) error
	RemoveAppRoutes(ctx context.Context, appID uint) error
}

// SetRouteSyncer wires the route reconciler used by canary traffic changes.
func (s *Service) SetRouteSyncer(rs RouteSyncer) { s.routeSync = rs }

// SetSettings wires the platform settings provider used to enforce resource caps.
func (s *Service) SetSettings(p *settings.Provider) { s.settings = p }

// ErrResourceCap is returned when a requested CPU/memory exceeds a platform cap.
var ErrResourceCap = errors.New("requested resources exceed the platform limit")

const bytesPerMB = 1024 * 1024

func (s *Service) validateResources(memoryBytes, nanoCPUs int64) error {
	// Negative limits would otherwise reach the Docker API, whether or not caps are set.
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

// Pure (no provider) so it can be unit-tested directly.
func checkResourceCaps(maxMemoryMB, maxCPUCores int, memoryBytes, nanoCPUs int64) error {
	if maxMemoryMB > 0 && memoryBytes > int64(maxMemoryMB)*bytesPerMB {
		return fmt.Errorf("%w: requested %d MB of memory exceeds the platform limit of %d MB", ErrResourceCap, memoryBytes/bytesPerMB, maxMemoryMB)
	}
	if maxCPUCores > 0 && nanoCPUs > int64(maxCPUCores)*1_000_000_000 {
		return fmt.Errorf("%w: requested %.2f CPU cores exceeds the platform limit of %d", ErrResourceCap, float64(nanoCPUs)/1e9, maxCPUCores)
	}
	return nil
}

func (s *Service) ResourceLimits() (maxCPUCores, maxMemoryMB int) {
	if s.settings == nil {
		return 0, 0
	}
	return s.settings.Int(settings.KeyMaxCPUCores, 0), s.settings.Int(settings.KeyMaxMemoryMB, 0)
}

// Healthcheck is an app's container health probe, as a create caller states it.
type Healthcheck struct {
	Type                                                               models.HealthcheckType
	Path, Command                                                      string
	Port, IntervalSeconds, TimeoutSeconds, Retries, StartPeriodSeconds int
}

// applyTo copies the probe onto the app, then fills the defaults for the timing left unset.
func (h *Healthcheck) applyTo(app *models.Application) {
	if h == nil {
		return
	}
	app.HealthcheckType, app.HealthcheckHTTPPath, app.HealthcheckPort, app.HealthcheckCommand = h.Type, h.Path, h.Port, h.Command
	app.HealthcheckIntervalSeconds, app.HealthcheckTimeoutSeconds = h.IntervalSeconds, h.TimeoutSeconds
	app.HealthcheckRetries, app.HealthcheckStartPeriodSeconds = h.Retries, h.StartPeriodSeconds
	normalizeHealthcheck(app)
}

func normalizeHealthcheck(app *models.Application) {
	if !models.ValidHealthcheckType(app.HealthcheckType) {
		app.HealthcheckType = models.HealthcheckImage
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

// Narrowed to the one method the storage guards need so they can be tested against a fake.
type volumeLookup interface {
	FindInWorkspace(workspaceID, id uint) (*models.Volume, error)
	// FindByDockerName resolves a mount that carries only the Docker name, so the data guard can judge it.
	FindByDockerName(name string) (*models.Volume, error)
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

func (s *Service) validateStack(workspaceID uint, stackID *uint) (*models.Stack, error) {
	if stackID == nil {
		return nil, nil
	}
	st, err := s.stacks.FindInWorkspace(workspaceID, *stackID)
	if err != nil {
		return nil, ErrStackNotFound
	}
	return st, nil
}

func (s *Service) sameLocation(a, b uint) bool {
	if a == models.DefaultClusterID {
		a = s.clusterOf(0)
	}
	if b == models.DefaultClusterID {
		b = s.clusterOf(0)
	}
	return a == b
}

// Membership that predates locations is left alone even when the stack sits elsewhere.
func (s *Service) joinsStack(app *models.Application) bool {
	prev, err := s.apps.FindByID(app.ID)
	return err != nil || prev.StackID == nil || app.StackID == nil || *prev.StackID != *app.StackID
}

// PortSpec describes a container port to declare on an application.
type PortSpec struct {
	ContainerPort int
	Protocol      string
	Scheme        string
	Name          string
}

// SetPorts keeps the primary Port field in sync with the first declared port.
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

// ReconcilePorts replaces the app's ports; a spec with no name keeps the name its live port has.
func (s *Service) ReconcilePorts(app *models.Application, specs []PortSpec) error {
	live, err := s.ports.ListByApp(app.ID)
	if err != nil {
		return err
	}
	// Saving the app would otherwise write the preloaded rows back over the replacement.
	app.Ports = nil
	if err := s.SetPorts(app, KeepPortNames(specs, live)); err != nil {
		return err
	}
	app.Ports, err = s.ports.ListByApp(app.ID)
	return err
}

// KeepPortNames fills each unnamed spec with the name of the live port on the same number and protocol.
func KeepPortNames(specs []PortSpec, live []models.AppPort) []PortSpec {
	type key struct {
		port  int
		proto string
	}
	names := map[key]string{}
	for _, p := range live {
		names[key{p.ContainerPort, portProto(p.Protocol)}] = p.Name
	}
	out := make([]PortSpec, len(specs))
	for i, sp := range specs {
		if sp.Name == "" {
			sp.Name = names[key{sp.ContainerPort, portProto(sp.Protocol)}]
		}
		out[i] = sp
	}
	return out
}

func portProto(p string) string {
	if p == "udp" {
		return "udp"
	}
	return "tcp"
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
	// Hostname is stable across redeploys; StackHostname is empty when ungrouped.
	Hostname         string    `json:"hostname"`
	StackHostname    string    `json:"stack_hostname,omitempty"`
	RedeployRequired bool      `json:"redeploy_required"`
	VolumesCount     int       `json:"volumes_count"`
	RoutesCount      int       `json:"routes_count"`
	NetworksCount    int       `json:"networks_count"`
	EnvCount         int       `json:"env_count"`
	CreatedAt        time.Time `json:"created_at"`
	// LastDeploy is the last deployment that shipped, so it reads as when the running code last changed.
	LastDeploy *LastDeploy `json:"last_deploy,omitempty"`
	// Separate from LastDeploy: a restart runs the same release and must not read as a new rollout.
	LastLifecycle *LastLifecycle    `json:"last_lifecycle,omitempty"`
	RecentEvents  []models.AppEvent `json:"recent_events"`
}

// LastLifecycle is the last start, stop or restart, and who asked for it.
type LastLifecycle struct {
	Action   string    `json:"action"` // start | stop | restart
	At       time.Time `json:"at"`
	ByUserID *uint     `json:"by_user_id,omitempty"`
	ByName   string    `json:"by_name,omitempty"`
}

type LastDeploy struct {
	ID       uint      `json:"id"`
	Number   int       `json:"number"`
	At       time.Time `json:"at"`
	Trigger  string    `json:"trigger,omitempty"`
	ByUserID *uint     `json:"by_user_id,omitempty"`
	ByName   string    `json:"by_name,omitempty"`
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
	if dep, err := s.deployments.LatestSucceededByApp(app.ID); err == nil && dep != nil {
		at := dep.CreatedAt
		if dep.FinishedAt != nil {
			at = *dep.FinishedAt
		}
		ov.LastDeploy = &LastDeploy{
			ID: dep.ID, Number: dep.Number, At: at, Trigger: dep.Trigger,
			ByUserID: dep.TriggeredByID, ByName: dep.TriggeredByName,
		}
	}
	if app.LastLifecycleAt != nil && app.LastLifecycleAction != "" {
		ll := &LastLifecycle{Action: app.LastLifecycleAction, At: *app.LastLifecycleAt, ByUserID: app.LastLifecycleByID}
		if app.LastLifecycleByID != nil {
			ll.ByName, _ = s.apps.LifecycleActorName(app.ID)
		}
		ov.LastLifecycle = ll
	}
	if evts, err := s.appEvents.ListByApp(app.ID, 6, 0); err == nil {
		ov.RecentEvents = evts
	}
	return ov
}

// LiveStatus is derived from Docker inspect, not the stored deploy-time status.
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
	// Zero for container apps.
	ServiceReplicas     uint64 `json:"service_replicas,omitempty"`
	ServiceRunningTasks uint64 `json:"service_running_tasks,omitempty"`
}

// LiveStatus falls back to the stored status when there is no running container.
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

// A service has no single container to inspect, so status is derived from task convergence.
func (s *Service) serviceLiveStatus(ctx context.Context, app *models.Application, ls LiveStatus) LiveStatus {
	mgr, err := s.swarmManager(ctx, app)
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
	// Desired replicas are the truth: 0 means stopped, otherwise Swarm is converging, so never "exited".
	switch {
	case app.Status == models.AppStatusStopped || st.Replicas == 0:
		ls.Status = "stopped"
	case st.RunningTasks >= st.Replicas:
		ls.Status = "running"
	default:
		ls.Status = "starting"
	}
	// Uptime comes from the swarm task, which may sit on a node Miabi has no Docker client for.
	ls.StartedAt = st.StartedAt
	if ls.Running && ls.StartedAt != "" {
		if t, perr := time.Parse(time.RFC3339Nano, ls.StartedAt); perr == nil {
			ls.UptimeSeconds = int64(time.Since(t).Seconds())
		}
	}
	// There is no manager-side service stats, so this only works on nodes Miabi has a client for.
	if ls.Running {
		if dc, cid, cerr := s.clients.ForServiceTask(ctx, app.ClusterID, node.AppAlias(app)); cerr == nil {
			if sample, serr := dc.StatsOnce(ctx, cid); serr == nil {
				ls.Stats = &sample
			}
		}
	}
	return ls
}

// intentionallyStopped distinguishes a user-stopped app from a crash.
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
	s.emitSeverity(app, t, models.SeverityInfo, message)
}

// For outcomes the user should notice that aren't failures of the requested operation.
func (s *Service) emitSeverity(app *models.Application, t models.AppEventType, sev models.AppEventSeverity, message string) {
	if s.events == nil {
		return
	}
	s.events.Record(&models.AppEvent{
		WorkspaceID: app.WorkspaceID, ApplicationID: app.ID,
		Type: t, Severity: sev, Message: message,
	})
}

// The mark vouches for that image only; a tag change is not a repoint, so upgrades keep it.
func (s *Service) clearStaleOfficialTemplate(app *models.Application) {
	if !app.OfficialTemplate || app.ID == 0 {
		return
	}
	prev, err := s.apps.FindByID(app.ID)
	if err != nil {
		return
	}
	if prev.SourceType != app.SourceType ||
		!strings.EqualFold(strings.TrimSpace(prev.Image), strings.TrimSpace(app.Image)) ||
		!strings.EqualFold(strings.TrimSpace(prev.GitRepo), strings.TrimSpace(app.GitRepo)) {
		app.OfficialTemplate = false
	}
}

// A tag in the image ref wins over a separate tag; digest-pinned refs are kept whole.
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
	}
	if in.SourceType == "" {
		in.SourceType = models.AppSourceImage
	}
	if in.SourceType == models.AppSourceImage {
		in.Image, in.Tag = normalizeImageTag(in.Image, in.Tag)
	}
	// A selected saved repository supplies the clone URL, so only require one when none is attached.
	if in.SourceType == models.AppSourceGit && strings.TrimSpace(in.GitRepo) == "" && in.GitRepositoryID == nil {
		return nil, ErrGitRepoRequired
	}
	if in.SourceType == models.AppSourceImage && strings.TrimSpace(in.Image) == "" {
		return nil, ErrImageRequired
	}
	if err := s.checkImage(workspaceID, in.Image, in.Tag); err != nil {
		return nil, err
	}
	if err := validateBuildConfig(in.SourceType, in.BuildMethod, in.Builder, in.Buildpacks, in.BuildEnv); err != nil {
		return nil, err
	}
	if err := s.customBuilderAllowed(workspaceID, in.Builder); err != nil {
		return nil, err
	}
	if st, err := s.validateStack(workspaceID, in.StackID); err != nil {
		return nil, err
	} else if st != nil && !s.sameLocation(st.ClusterID, s.clusterOf(in.ServerID)) {
		return nil, ErrStackLocation
	}
	mem, cpu, err := s.quota.ResolveAppResources(workspaceID, in.MemoryBytes, in.NanoCPUs)
	if err != nil {
		return nil, err
	}
	in.MemoryBytes, in.NanoCPUs = mem, cpu
	if err := s.validateResources(in.MemoryBytes, in.NanoCPUs); err != nil {
		return nil, err
	}
	if err := validateGPUCount(in.GPUCount); err != nil {
		return nil, err
	}
	// Device passthrough is privileged, so GPU is hard-gated at save; re-checked at deploy.
	if err := s.gpuAllowed(workspaceID, in.GPUCount); err != nil {
		return nil, err
	}
	runAsUser, err := s.validRunAsUser(workspaceID, in.RunAsUser, false)
	if err != nil {
		return nil, err
	}
	addCaps, err := s.validCapabilities(workspaceID, in.AddCapabilities)
	if err != nil {
		return nil, err
	}
	devices, err := s.validDevices(workspaceID, in.Devices)
	if err != nil {
		return nil, err
	}
	dropCaps, err := validDropCapabilities(addCaps, in.DropCapabilities)
	if err != nil {
		return nil, err
	}
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
		ClusterID: s.clusterOf(serverID),
		Icon:      in.Icon,
		Image:     in.Image, Tag: in.Tag, GitRepo: in.GitRepo, GitRef: in.GitRef,
		BuildMethod: buildMethodForSource(in.SourceType, in.BuildMethod),
		Builder:     in.Builder, Buildpacks: in.Buildpacks, BuildEnv: in.BuildEnv,
		RegistryID: in.RegistryID, GitRepositoryID: in.GitRepositoryID, StackID: in.StackID,
		Command: in.Command, Port: in.Port,
		MemoryBytes: in.MemoryBytes, NanoCPUs: in.NanoCPUs,
		GPUCount: in.GPUCount, GPUKind: strings.TrimSpace(in.GPUKind),
		RunAsUser:              runAsUser,
		AddCapabilities:        addCaps,
		Devices:                devices,
		DropCapabilities:       dropCaps,
		ReadOnlyRootFilesystem: in.ReadOnlyRootFilesystem,
		NoNewPrivileges:        in.NoNewPrivileges,
		DeployStrategy:         validStrategyOrDefault(in.DeployStrategy),
		RestartPolicy:          normalizeRestartPolicy(in.RestartPolicy),
		ImagePullPolicy:        normalizeImagePullPolicy(in.ImagePullPolicy),
		RuntimeKind:            in.RuntimeKind,
		Replicas:               in.Replicas,
		PlacementConstraints:   in.PlacementConstraints,
		UpdateConfig:           in.UpdateConfig,
		Status:                 models.AppStatusCreated,
		Metadata:               models.DefaultManagedBy(in.Metadata, models.ManagedByUser),
		Annotations:            in.Annotations,
		ContainerLabels:        docker.SanitizeUserLabels(in.ContainerLabels),
	}
	in.Healthcheck.applyTo(app)
	if app.HealthcheckType == "" {
		app.HealthcheckType = models.HealthcheckImage
	}
	normalizeRuntime(app)
	// Only interactive creates default to a service; normalizeRuntime already filled the kind, so branch on input.
	interactive := app.Metadata[models.MetaManagedBy] == models.ManagedByUser
	serviceOK := s.isSwarm(app.ClusterID) && s.quota.Require(workspaceID, quota.CapServiceRuntime) == nil
	if defaultToServiceRuntime(in.RuntimeKind, serviceOK, interactive) {
		app.RuntimeKind = models.RuntimeService
		app.Metadata = models.SetBuiltin(app.Metadata, models.MetaRuntimeAutoService, "true")
	}
	if err := s.validateRuntime(app, ""); err != nil {
		// For the cluster-mode default, degrade to a container rather than failing a create nobody asked to be a service.
		if in.RuntimeKind != "" {
			return nil, err
		}
		app.RuntimeKind = models.RuntimeContainer
		delete(app.Metadata, models.MetaRuntimeAutoService)
	}
	if err := s.checkCompute(app, app.Replicas); err != nil {
		return nil, err
	}
	if err := s.apps.Create(app); err != nil {
		return nil, err
	}
	// Persisted so the alias stays fixed across redeploys: it is the reverse-proxy upstream.
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
	s.adoptRepoPipeline(app, in)
	return app, nil
}

// Failures are reported and swallowed: the app already exists and is deployable.
func (s *Service) adoptRepoPipeline(app *models.Application, in CreateInput) {
	if !in.UsePipeline || app.SourceType != models.AppSourceGit || s.pipelines == nil {
		return
	}
	if !s.repoPipelinesEnabled() {
		s.emitSeverity(app, models.EventPipelineAdopted, models.SeverityWarning,
			"Repository pipelines are disabled on this platform — building directly")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), repoPipelineAdoptTimeout)
	defer cancel()
	def, err := s.pipelines.AdoptForApp(ctx, app, in.UserID)
	switch {
	case err != nil:
		logger.Warn("could not adopt the repository's pipeline; the app will build directly",
			"app", app.Name, "workspace", app.WorkspaceID, "error", err)
		s.emitSeverity(app, models.EventPipelineAdopted, models.SeverityWarning,
			"Could not use the repository's pipeline ("+err.Error()+") — building directly")
	case def != nil:
		s.emitSeverity(app, models.EventPipelineAdopted, models.SeverityInfo,
			"Using the pipeline from "+def.SourcePath+" — deploys run through it")
	}
}

// Generous: the app is already created, so overshooting costs a slow create, not a failed one.
const repoPipelineAdoptTimeout = 90 * time.Second

// Kill-switch because a repo pipeline chooses the images and commands a runner executes; unset means on.
func (s *Service) repoPipelinesEnabled() bool {
	if s.settings == nil {
		return true
	}
	return s.settings.Bool(settings.KeyRepoPipelinesEnabled, true)
}

func (s *Service) RepoPipelineForApp(appID uint) (*models.PipelineDefinition, error) {
	if s.pipelines == nil {
		return nil, nil
	}
	return s.pipelines.RepoPipelineForApp(appID)
}

// SetNetworks always includes the default network, which the app shares with its databases; a missing one is repaired.
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
	s.annotateGitOps(app)
	// List views skip this.
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
	s.annotateReplicas(apps)
	return apps, nil
}

// A slow manager costs the list its replica counts rather than its response.
const listReplicasTimeout = 3 * time.Second

// One service listing per cluster rather than an inspect per app; best-effort.
func (s *Service) annotateReplicas(apps []models.Application) {
	ctx, cancel := context.WithTimeout(context.Background(), listReplicasTimeout)
	defer cancel()
	byCluster := map[uint]map[string]docker.ServiceStatus{}
	for i := range apps {
		app := &apps[i]
		if app.RuntimeKind != models.RuntimeService || !s.isSwarm(app.ClusterID) {
			continue
		}
		services, ok := byCluster[app.ClusterID]
		if !ok {
			services = map[string]docker.ServiceStatus{}
			if mgr, err := s.swarmManager(ctx, app); err == nil {
				if list, err := mgr.ServiceList(ctx); err == nil {
					for _, st := range list {
						services[st.Name] = st
					}
				}
			}
			byCluster[app.ClusterID] = services
		}
		if st, found := services[node.AppAlias(app)]; found {
			running, desired := st.RunningTasks, st.Replicas
			app.RunningReplicas, app.DesiredReplicas = &running, &desired
		}
	}
}

// AppsReferencingSecret satisfies secret.Consumers.
func (s *Service) AppsReferencingSecret(workspaceID uint, name string) ([]models.Application, error) {
	apps, err := s.apps.ListByWorkspaceWithEnv(workspaceID)
	if err != nil {
		return nil, err
	}
	stacks := map[uint][]models.StackEnvVar{}
	out := make([]models.Application, 0)
	for i := range apps {
		if s.appReferencesSecret(&apps[i], name, stacks) {
			out = append(out, apps[i])
		}
	}
	return out, nil
}

// Covers own env, stack shared env, and mounted configs; stacks caches each stack's env for one scan.
func (s *Service) appReferencesSecret(app *models.Application, name string, stacks map[uint][]models.StackEnvVar) bool {
	own := make(map[string]bool, len(app.EnvVars))
	for _, ev := range app.EnvVars {
		own[ev.Key] = true
		if envReferencesSecret(ev.Value, ev.IsSecret, name) {
			return true
		}
	}
	if app.StackID != nil && s.stackEnv != nil {
		shared, ok := stacks[*app.StackID]
		if !ok {
			shared, _ = s.stackEnv.ListByStack(*app.StackID)
			stacks[*app.StackID] = shared
		}
		for _, ev := range shared {
			// An app-level variable of the same key wins at deploy, so the stack's is inert here.
			if !own[ev.Key] && envReferencesSecret(ev.Value, ev.IsSecret, name) {
				return true
			}
		}
	}
	if s.configs == nil {
		return false
	}
	for _, mt := range app.Mounts {
		if mt.ConfigID == 0 {
			continue
		}
		cfg, err := s.configs.Get(app.WorkspaceID, mt.ConfigID)
		if err != nil {
			continue
		}
		data, derr := s.configs.Data(cfg)
		if derr != nil {
			continue
		}
		for _, ref := range configsvc.SecretRefNames(data) {
			if ref == name {
				return true
			}
		}
	}
	return false
}

// A secret-flagged value is stored encrypted, so it is read decrypted.
func envReferencesSecret(value string, isSecret bool, name string) bool {
	if isSecret {
		dec, err := crypto.Decrypt(value)
		if err != nil {
			return false
		}
		value = dec
	}
	return secret.ReferencesSecret(value, name)
}

func (s *Service) AppsMountingConfig(workspaceID, configID uint) ([]models.Application, error) {
	apps, err := s.apps.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]models.Application, 0)
	for i := range apps {
		for _, mt := range apps[i].Mounts {
			if mt.ConfigID == configID {
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
	s.clearStaleOfficialTemplate(app)
	if err := s.checkImage(app.WorkspaceID, app.Image, app.Tag); err != nil {
		return err
	}
	if st, err := s.validateStack(app.WorkspaceID, app.StackID); err != nil {
		return err
	} else if st != nil && !s.sameLocation(st.ClusterID, app.ClusterID) && s.joinsStack(app) {
		return ErrStackLocation
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
	mem, cpu, err := s.quota.ResolveAppResources(app.WorkspaceID, app.MemoryBytes, app.NanoCPUs)
	if err != nil {
		return err
	}
	app.MemoryBytes, app.NanoCPUs = mem, cpu
	if err := s.validateResources(app.MemoryBytes, app.NanoCPUs); err != nil {
		return err
	}
	if err := validateGPUCount(app.GPUCount); err != nil {
		return err
	}
	// Re-gate GPU so a workspace can't keep GPU access by editing an app after a plan downgrade.
	app.GPUKind = strings.TrimSpace(app.GPUKind)
	if err := s.gpuAllowed(app.WorkspaceID, app.GPUCount); err != nil {
		return err
	}
	// Re-validated on every update so a stored run-as user can't survive a plan that mandates non-root.
	runAsUser, err := s.validRunAsUser(app.WorkspaceID, app.RunAsUser, app.OfficialTemplate)
	if err != nil {
		return err
	}
	app.RunAsUser = runAsUser
	// Re-validated so revoking a workspace's privilege strips grants on next write.
	if app.AddCapabilities, err = s.validCapabilities(app.WorkspaceID, app.AddCapabilities); err != nil {
		return err
	}
	if app.Devices, err = s.validDevices(app.WorkspaceID, app.Devices); err != nil {
		return err
	}
	if app.DropCapabilities, err = validDropCapabilities(app.AddCapabilities, app.DropCapabilities); err != nil {
		return err
	}
	normalizeDeployConfig(app)
	normalizeHealthcheck(app)
	// The app arrives already mutated, so the previous runtime has to be read back.
	stored := models.RuntimeKind("")
	if current, err := s.apps.FindByID(app.ID); err == nil {
		stored = current.RuntimeKind
	}
	if err := s.validateRuntime(app, stored); err != nil {
		return err
	}
	if err := s.checkCompute(app, app.Replicas); err != nil {
		return err
	}
	// GitOps sets labels directly and relies on this sanitize.
	app.ContainerLabels = docker.SanitizeUserLabels(app.ContainerLabels)
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventSettingsUpdated, "Settings updated")
	return nil
}

// normalizeDeployConfig doesn't run on create, and a raw unknown value would deploy as the switch fallthrough.
func validStrategyOrDefault(s models.DeployStrategy) models.DeployStrategy {
	if models.ValidDeployStrategy(s) {
		return s
	}
	return models.DeployRolling
}

func normalizeDeployConfig(app *models.Application) {
	if !models.ValidDeployStrategy(app.DeployStrategy) {
		app.DeployStrategy = models.DeployRolling
	}
	app.RestartPolicy = normalizeRestartPolicy(app.RestartPolicy)
	app.ImagePullPolicy = normalizeImagePullPolicy(app.ImagePullPolicy)
	if !models.ValidCanaryMode(app.CanaryMode) {
		app.CanaryMode = models.CanaryModeAuto
	}
	app.CanaryInitialWeight = clamp(app.CanaryInitialWeight, 1, 99)
	app.CanaryStepWeight = clamp(app.CanaryStepWeight, 1, 99)
	if app.CanaryStepIntervalSeconds < 10 {
		app.CanaryStepIntervalSeconds = 10
	}
	normalizeRuntime(app)
}

// Declarative sources are excluded so a manifest must ask for a service on purpose.
func defaultToServiceRuntime(explicit models.RuntimeKind, clusterOn, interactive bool) bool {
	return explicit == "" && clusterOn && interactive
}

func normalizeRuntime(app *models.Application) {
	if !models.ValidRuntimeKind(app.RuntimeKind) {
		app.RuntimeKind = models.RuntimeContainer
	}
	if app.Replicas < 1 {
		app.Replicas = 1
	}
}

// stored is the existing runtime: the rule applies to the change, so a service can always be edited back out.
func (s *Service) validateRuntime(app *models.Application, stored models.RuntimeKind) error {
	if app.RuntimeKind == models.RuntimeService && !s.isSwarm(app.ClusterID) {
		switch {
		case app.Metadata[models.MetaRuntimeAutoService] == "true":
			// The platform chose this runtime, so it un-chooses it, as create and reconcileAutoRuntime do.
			app.RuntimeKind = models.RuntimeContainer
			delete(app.Metadata, models.MetaRuntimeAutoService)
		case stored != models.RuntimeService:
			return ErrClusterDisabled
		}
	}
	if app.RuntimeKind == models.RuntimeService && stored != models.RuntimeService {
		if err := s.quota.Require(app.WorkspaceID, quota.CapServiceRuntime); err != nil {
			return err
		}
	}
	if app.Replicas > MaxReplicas {
		return ErrTooManyReplicas
	}
	if app.RuntimeKind == models.RuntimeService && len(app.Devices) > 0 {
		return models.ErrDevicesOnService
	}
	return s.requireSharedStorage(app, app.Replicas)
}

// A container app always counts once; its stored contribution is excluded.
func (s *Service) checkCompute(app *models.Application, replicas int) error {
	n := int64(1)
	if app.RuntimeKind == models.RuntimeService && replicas > 1 {
		n = int64(replicas)
	}
	return s.quota.CheckComputeAdd(app.WorkspaceID, app.NanoCPUs*n, app.MemoryBytes*n, app.ID)
}

// resolveMountVolume reads a mount's volume for the storage guards.
func (s *Service) resolveMountVolume(workspaceID, volumeID uint) (*models.Volume, error) {
	if s.volumes == nil {
		return nil, ErrVolumeUnverifiable
	}
	v, err := s.volumes.FindInWorkspace(workspaceID, volumeID)
	switch {
	case err == nil:
		return v, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrVolumeNotFound
	default:
		return nil, fmt.Errorf("%w (volume %d: %v)", ErrVolumeUnverifiable, volumeID, err)
	}
}

// Host-path binds never follow a task to another node; rwo volumes are rejected once replicas > 1.
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
		v, err := s.resolveMountVolume(app.WorkspaceID, m.VolumeID)
		if err != nil {
			return err
		}
		if v.AccessMode != models.AccessRWX {
			return ErrLocalVolumeReplicated
		}
	}
	return nil
}

func (s *Service) hasNodeLocalStorage(app *models.Application) bool {
	for _, m := range app.Mounts {
		if m.HostPreset != "" {
			return true // privileged host bind: node-local
		}
		if m.VolumeID == 0 {
			continue
		}
		v, err := s.resolveMountVolume(app.WorkspaceID, m.VolumeID)
		if err != nil || v.AccessMode != models.AccessRWX {
			return true // rwo managed volume, or one we cannot read: treat as node-local
		}
	}
	return false
}

// One-shot: the marker clears so a later explicit choice is respected.
func (s *Service) reconcileAutoRuntime(appID uint) {
	app, err := s.apps.FindByID(appID)
	if err != nil || app.Metadata[models.MetaRuntimeAutoService] != "true" {
		return
	}

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

func normalizeRestartPolicy(p models.RestartPolicy) models.RestartPolicy {
	if !models.ValidRestartPolicy(p) {
		return models.RestartUnlessStopped
	}
	return p
}

func validateBuildConfig(sourceType models.AppSourceType, method models.AppBuildMethod, builder string, buildpacks []string, buildEnv map[string]string) error {
	if sourceType != models.AppSourceGit {
		// auto is the stored default, so only an explicit non-auto method is rejected on image apps.
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

func normalizeBuildMethod(m models.AppBuildMethod) models.AppBuildMethod {
	if !models.ValidAppBuildMethod(m) {
		return models.BuildAuto
	}
	return m
}

func buildMethodForSource(sourceType models.AppSourceType, m models.AppBuildMethod) models.AppBuildMethod {
	if sourceType != models.AppSourceGit {
		return models.BuildAuto
	}
	return normalizeBuildMethod(m)
}

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

var ErrNoActiveContainer = errors.New("application has no active container")

// Docker would create an empty volume in place of missing data, and Miabi never restores on its own.
func (s *Service) guardData(ctx context.Context, app *models.Application) error {
	return datavolume.CheckApp(ctx, s.clients, s.volumes, app)
}

// Docker offers no manager-side exec, so this is a hard limit; logs are unaffected.
var ErrTaskOnUnmanagedNode = errors.New(
	"this app's task runs on a swarm node with no Miabi agent, so a shell cannot be opened. " +
		"Add the node to Miabi (install the agent) to use exec")

func (s *Service) EnsureExecAllowed(workspaceID uint) error {
	return s.quota.Require(workspaceID, quota.CapShellExec)
}

// The caller owns the returned stream and must Close it.
func (s *Service) Exec(ctx context.Context, app *models.Application, opts docker.ExecOptions) (docker.ExecStream, error) {
	cid, dc, err := s.runtimeContainerID(ctx, app)
	if err != nil {
		return nil, ErrNoActiveContainer
	}
	return dc.Exec(ctx, cid, opts)
}

// Never caller-supplied: ps runs as root and a flag like `e` would print process environments, secrets included.
const processListArgs = "aux"

func (s *Service) Processes(ctx context.Context, app *models.Application) (docker.ProcessList, error) {
	cid, dc, err := s.runtimeContainerID(ctx, app)
	if err != nil {
		return docker.ProcessList{}, ErrNoActiveContainer
	}
	return dc.Top(ctx, cid, processListArgs)
}

func (s *Service) activeContainerID(appID uint) (string, error) {
	rel, err := s.releases.FindActive(appID)
	if err != nil || rel.ContainerID == "" {
		return "", ErrNotDeployable
	}
	return rel.ContainerID, nil
}

func (s *Service) runtimeContainerID(ctx context.Context, app *models.Application) (string, docker.Client, error) {
	if app.RuntimeKind == models.RuntimeService {
		dc, cid, err := s.clients.ForServiceTask(ctx, app.ClusterID, node.AppAlias(app))
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

// Start redeploys instead when config changed while stopped, returning that deployment.
func (s *Service) Start(ctx context.Context, app *models.Application) (*models.Deployment, error) {
	if err := s.guardData(ctx, app); err != nil {
		return nil, err
	}
	if app.RedeployRequired {
		return s.Redeploy(app)
	}
	if app.RuntimeKind == models.RuntimeService {
		// Start = scale the service back to its desired replica count.
		replicas := uint64(app.Replicas)
		if replicas < 1 {
			replicas = 1
		}
		mgr, err := s.swarmManager(ctx, app)
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

// For a service app Stop scales to zero; the desired count stays recorded so Start restores it.
func (s *Service) Stop(ctx context.Context, app *models.Application) error {
	// Persist intent first: a die event from a non-graceful exit (137) would otherwise read as a crash.
	prev := app.Status
	_ = s.apps.SetStatus(app.ID, models.AppStatusStopped)
	if app.RuntimeKind == models.RuntimeService {
		mgr, err := s.swarmManager(ctx, app)
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

// Restart redeploys instead when config changed while stopped, returning that deployment.
func (s *Service) Restart(ctx context.Context, app *models.Application) (*models.Deployment, error) {
	if err := s.guardData(ctx, app); err != nil {
		return nil, err
	}
	if app.RedeployRequired {
		return s.Redeploy(app)
	}
	if app.RuntimeKind == models.RuntimeService {
		// Restart = force a rolling restart of the service's tasks in place.
		mgr, err := s.swarmManager(ctx, app)
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

// Called by the handler since only the HTTP edge knows the actor; best-effort, as it is display-only.
func (s *Service) RecordLifecycle(appID uint, action string, by *uint) {
	if err := s.apps.SetLastLifecycle(appID, action, time.Now(), by); err != nil {
		logger.Warn("could not record the app lifecycle action", "app", appID, "action", action, "error", err)
	}
}

func (s *Service) Scale(ctx context.Context, app *models.Application, replicas int) error {
	if app.RuntimeKind != models.RuntimeService {
		return ErrNotService
	}
	if !s.isSwarm(app.ClusterID) {
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
	if err := s.checkCompute(app, replicas); err != nil {
		return err
	}
	mgr, err := s.swarmManager(ctx, app)
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

// Readiness gate for rolling restarts.
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

// Refuses while running to avoid yanking a live workload.
func (s *Service) Delete(ctx context.Context, app *models.Application) error {
	if s.LiveStatus(ctx, app).Running {
		return ErrAppRunning
	}
	// Record before the row is gone; Record publishes synchronously so the live stream still sees it.
	s.emit(app, models.EventAppDeleted, "Application deleted")
	if rel, err := s.releases.FindActive(app.ID); err == nil && rel.ContainerID != "" {
		// Recorded first so die and destroy events read as a deliberate stop rather than a crash.
		_ = s.apps.SetStatus(app.ID, models.AppStatusStopped)
		_ = s.eng(app).StopContainer(ctx, rel.ContainerID, 10)
		_ = s.eng(app).RemoveContainer(ctx, rel.ContainerID, true)
	}
	if s.routeSync != nil {
		_ = s.routeSync.RemoveAppRoutes(ctx, app.ID)
	}
	if s.portBindings != nil {
		_ = s.portBindings.DeleteByApp(app.ID)
	}
	if s.pipelines != nil {
		if err := s.pipelines.DeleteForApp(app.WorkspaceID, app.ID); err != nil {
			logger.Warn("could not clean up the app's pipelines", "app", app.Name, "error", err)
		}
	}
	return s.apps.Delete(app.ID)
}

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

// Later duplicate keys win.
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

// Projection is per file, so this never becomes a directory bind and is always read-only.
func (s *Service) AttachConfig(app *models.Application, configID uint, key, path, mode string) error {
	if path == "" {
		return ErrMountPathRequired
	}
	// Validated here: a bad config or key would otherwise fail silently in the runtime.
	if err := s.validateConfigMount(app.WorkspaceID, configID, key); err != nil {
		return err
	}
	for i, m := range app.Mounts {
		if m.ConfigID == configID && m.ConfigKey == key {
			app.Mounts[i].Path, app.Mounts[i].Mode = path, mode
			return s.apps.Update(app)
		}
	}
	app.Mounts = append(app.Mounts, models.AppMount{
		ConfigID: configID, ConfigKey: key, Path: path, Mode: mode, ReadOnly: true,
	})
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventConfigAttached, "Config file mounted at "+path)
	return nil
}

// A nil config reader skips the check rather than blocking the mount.
func (s *Service) validateConfigMount(workspaceID, configID uint, key string) error {
	if s.configs == nil {
		return nil
	}
	cfg, err := s.configs.Get(workspaceID, configID)
	if err != nil {
		return ErrConfigNotFound
	}
	if key == "" {
		return nil // the whole config projects under Path; every key is in scope
	}
	data, err := s.configs.Data(cfg)
	if err != nil {
		// Unreadable content cannot disprove the key; the config page reports decryption problems.
		return nil
	}
	if _, ok := data[key]; !ok {
		return ErrConfigKeyNotFound
	}
	return nil
}

// An empty key removes the whole-config mount.
func (s *Service) DetachConfig(app *models.Application, configID uint, key string) error {
	out, removed := removeConfigMount(app.Mounts, configID, key)
	if !removed {
		return ErrMountNotFound
	}
	app.Mounts = out
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventConfigDetached, "Config file mount removed")
	return nil
}

// The same config can be mounted once per file, so matching on the id alone would remove them all.
func removeConfigMount(mounts []models.AppMount, configID uint, key string) ([]models.AppMount, bool) {
	out := make([]models.AppMount, 0, len(mounts))
	removed := false
	for _, m := range mounts {
		if m.ConfigID == configID && m.ConfigKey == key {
			removed = true
			continue
		}
		out = append(out, m)
	}
	return out, removed
}

// AttachVolume mounts a workspace volume into the app at path. Takes effect on the next deploy.
func (s *Service) AttachVolume(app *models.Application, volumeID uint, path string) error {
	vol, err := s.resolveMountVolume(app.WorkspaceID, volumeID)
	if err != nil {
		return err
	}
	if !s.sameLocation(vol.ClusterID, app.ClusterID) {
		return ErrVolumeLocation
	}
	if nodeLocal(vol) {
		if app.RuntimeKind == models.RuntimeService {
			// Deploy pins the task to its node-local volumes' node, so they must all share one node.
			if n := s.localMountNode(app, vol.ID); n != 0 && n != vol.ServerID {
				return ErrNodeMismatch
			}
		} else if vol.ServerID != app.ServerID {
			return ErrNodeMismatch
		}
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
	// The volume is immutable, so the denormalized host path can't drift.
	app.Mounts = append(app.Mounts, models.AppMount{VolumeID: vol.ID, DockerName: vol.DockerName, Path: path, HostPath: vol.HostPath})
	if err := s.apps.Update(app); err != nil {
		return err
	}
	s.emit(app, models.EventVolumeAttached, "Volume attached at "+path)
	return nil
}

// Host-path volumes exist on every node and rwx ones mount from their backend wherever the task runs.
func nodeLocal(v *models.Volume) bool {
	return v.Driver != models.VolumeDriverHost && v.AccessMode != models.AccessRWX
}

// localMountNode is the node of the app's other node-local volumes, or 0 when it mounts none.
func (s *Service) localMountNode(app *models.Application, except uint) uint {
	for _, m := range app.Mounts {
		if m.VolumeID == 0 || m.VolumeID == except {
			continue
		}
		if v, err := s.volumes.FindInWorkspace(app.WorkspaceID, m.VolumeID); err == nil && nodeLocal(v) {
			return v.ServerID
		}
	}
	return 0
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

// AttachHostMount resolves the host source from the preset at deploy time, never from the client.
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

// RevealEnvVar backs the audited reveal endpoint.
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

// Deploy is for machine paths, so no actor is recorded; a person's deploy goes through RequestDeploy.
func (s *Service) Deploy(app *models.Application, registryOverride *uint, tagOverride string, strategy models.DeployStrategy) (*models.Deployment, error) {
	return s.deploy(app, registryOverride, tagOverride, strategy, false, nil)
}

// Only the user-facing entry point offers no-cache: an internal redeploy must not silently pay for a cold build.
func (s *Service) deploy(app *models.Application, registryOverride *uint, tagOverride string, strategy models.DeployStrategy, noCache bool, triggeredBy *uint) (*models.Deployment, error) {

	s.reconcileAutoRuntime(app.ID)
	regID := app.RegistryID
	if registryOverride != nil {
		regID = registryOverride
	}

	image := ""
	if app.SourceType != models.AppSourceGit {
		image = app.ImageRef(tagOverride)
	}
	return s.enqueue(app.ID, app.ServerID, image, "manual", regID, s.resolveStrategy(app, strategy), noCache, triggeredBy)
}

// Nothing is deleted, so a build already in flight is unaffected.
func (s *Service) InvalidateBuildCache(app *models.Application) error {
	return s.apps.BumpCacheGeneration(app.ID)
}

// Exactly one of Deployment or PipelineRun is set.
type DeployResult struct {
	Deployment *models.Deployment
	Run        *models.PipelineRun
}

// A repo-owned pipeline is run instead so a deploy can never skip its test and scan steps.
func (s *Service) RequestDeploy(app *models.Application, registryOverride *uint, tagOverride string, strategy models.DeployStrategy, userID *uint, noCache bool) (*DeployResult, error) {
	def, err := s.RepoPipelineForApp(app.ID)
	if err != nil {
		return nil, fmt.Errorf("resolve the app's pipeline: %w", err)
	}
	if def != nil {
		run, err := s.pipelines.TriggerForApp(app, "manual", userID, noCache)
		if err != nil {
			return nil, err
		}
		return &DeployResult{Run: run}, nil
	}
	dep, err := s.deploy(app, registryOverride, tagOverride, strategy, noCache, userID)
	if err != nil {
		return nil, err
	}
	return &DeployResult{Deployment: dep}, nil
}

// Canary needs a running release, so a canary on a never-deployed app falls back to rolling.
func (s *Service) resolveStrategy(app *models.Application, requested models.DeployStrategy) models.DeployStrategy {
	return models.EffectiveDeployStrategy(app, requested)
}

func (s *Service) Redeploy(app *models.Application) (*models.Deployment, error) {
	s.reconcileAutoRuntime(app.ID)
	image := ""
	if app.SourceType != models.AppSourceGit {
		image = app.ImageRef("")
	}
	return s.enqueue(app.ID, app.ServerID, image, "auto", app.RegistryID, models.DeployRolling, false, nil)
}

// Deliberately the ordinary deploy path, so nothing is moved and the data guard still applies.
func (s *Service) ReconcileRedeploy(app *models.Application, reason string) (*models.Deployment, error) {
	image := ""
	if app.SourceType != models.AppSourceGit {
		image = app.ImageRef("")
	}
	logger.Info("control manager: redeploying an app whose workload disappeared",
		"app", app.Name, "app_id", app.ID, "node", app.ServerID, "reason", reason)
	return s.enqueue(app.ID, app.ServerID, image, "reconcile", app.RegistryID, models.DeployRolling, false, nil)
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

// The reserved-prefix protection lives in docker.SanitizeUserLabels.
const (
	maxContainerLabels  = 50
	maxLabelKeyLength   = 256
	maxLabelValueLength = 4096
)

// The error handlers read Code() off the returned error.
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

// A custom user cannot be the way back to root; re-run on every update so later tightening applies.
func (s *Service) validRunAsUser(workspaceID uint, v string, officialTemplate bool) (string, error) {
	clean, err := models.NormalizeRunAsUser(v)
	if err != nil {
		return "", err
	}
	if clean == "" {
		return "", nil
	}
	if s.quota.RequireNonRootUser(workspaceID, officialTemplate) && !models.RunAsUserIsNonRoot(clean) {
		return "", models.ErrRunAsUserRoot
	}
	return clean, nil
}

// A custom builder runs with docker access on the runner, so on shared runners it is an arbitrary-code vector.
func (s *Service) customBuilderAllowed(workspaceID uint, builder string) error {
	if strings.TrimSpace(builder) == "" || s.quota == nil {
		return nil
	}
	return s.quota.Require(workspaceID, quota.CapCustomBuilder)
}

// The upper bound is a sanity cap; no node exposes near this many cards.
func validateGPUCount(gpuCount int) error {
	if gpuCount < 0 || gpuCount > 64 {
		return ErrInvalidGPUCount
	}
	return nil
}

// nil quota (single-tenant) skips the gate.
func (s *Service) gpuAllowed(workspaceID uint, gpuCount int) error {
	if gpuCount <= 0 || s.quota == nil {
		return nil
	}
	return s.quota.Require(workspaceID, quota.CapGPU)
}

// Both the global kill-switch and the per-workspace plan capability must allow it.
func (s *Service) customLabelsAllowed(workspaceID uint) error {
	if s.settings != nil && !s.settings.Bool(settings.KeyCustomLabelsEnabled, true) {
		return ErrCustomLabelsDisabled
	}
	if s.quota != nil {
		return s.quota.Require(workspaceID, quota.CapCustomLabels)
	}
	return nil
}

// Reserved keys are rejected explicitly so an interactive edit gets a clear error rather than a silent strip.
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
func (s *Service) Rollback(app *models.Application, releaseID uint, triggeredBy *uint) (*models.Deployment, error) {
	rel, err := s.releases.FindByID(releaseID)
	if err != nil || rel.ApplicationID != app.ID {
		return nil, fmt.Errorf("release not found")
	}
	return s.enqueue(app.ID, app.ServerID, rel.Image, "rollback", app.RegistryID, models.DeployRolling, false, triggeredBy)
}

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

// 0 is allowed only for an exclusive canary; otherwise the rules would quietly route nothing.
func (s *Service) SetCanaryWeight(ctx context.Context, app *models.Application, weight int) error {
	if app.CanaryReleaseID == nil {
		return ErrNoCanary
	}
	if weight < 0 {
		weight = 0
	}
	if weight > 99 {
		weight = 99
	}
	if weight == 0 && !(app.CanaryExclusive && len(app.CanaryMatch) > 0) {
		return ErrCanaryPooledZeroWeight
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

// PauseCanary holds the ramp at the current weight.
func (s *Service) PauseCanary(app *models.Application) error {
	if app.CanaryReleaseID == nil {
		return ErrNoCanary
	}
	if app.CanaryPausedAt != nil {
		return nil
	}
	now := time.Now()
	if err := s.apps.SetCanaryPaused(app.ID, &now); err != nil {
		return err
	}
	app.CanaryPausedAt = &now
	s.emit(app, models.EventDeployStarted, fmt.Sprintf("Canary rollout paused at %d%%", app.CanaryWeight))
	return nil
}

// ResumeCanary hands the rollout back to the ramp from its current weight.
func (s *Service) ResumeCanary(app *models.Application) error {
	if app.CanaryReleaseID == nil {
		return ErrNoCanary
	}
	if app.CanaryPausedAt == nil {
		return nil
	}
	if err := s.apps.SetCanaryPaused(app.ID, nil); err != nil {
		return err
	}
	app.CanaryPausedAt = nil
	s.emit(app, models.EventDeployStarted, fmt.Sprintf("Canary rollout resumed from %d%%", app.CanaryWeight))
	return nil
}

func (s *Service) PromoteCanary(app *models.Application, triggeredBy *uint) (*models.Deployment, error) {
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
	return s.enqueue(app.ID, app.ServerID, image, "manual", app.RegistryID, models.DeployRolling, false, triggeredBy)
}

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

// The worker handles the promote and auto paths.
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

func (s *Service) enqueue(appID, serverID uint, image, trigger string, registryID *uint, strategy models.DeployStrategy, noCache bool, triggeredBy *uint) (*models.Deployment, error) {
	// Every deploy path starts a container mounting the volumes, so a lost volume must stop them here.
	if app, err := s.apps.FindByID(appID); err == nil {
		if derr := s.guardData(context.Background(), app); derr != nil {
			return nil, derr
		}
	}
	if !models.ValidDeployStrategy(strategy) {
		strategy = models.DeployRolling
	}
	dep := &models.Deployment{ApplicationID: appID, Image: image, Trigger: trigger, Strategy: strategy, RegistryID: registryID, Status: models.DeploymentPending, NoCache: noCache, TriggeredByID: triggeredBy}
	if err := s.deployments.Create(dep); err != nil {
		return nil, err
	}
	if err := s.producer.EnqueueDeploy(dep.ID, serverID); err != nil {
		return nil, err
	}
	return dep, nil
}

func (s *Service) ListDeployments(appID uint, limit int) ([]models.DeploymentWithActor, error) {
	deps, err := s.deployments.ListByAppWithActor(appID, limit)
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

// GetPipelineRun loads a pipeline run (for waiting on a pipeline-backed deploy).
func (s *Service) GetPipelineRun(workspaceID, id uint) (*models.PipelineRun, error) {
	if s.pipelines == nil {
		return nil, ErrPipelinesUnavailable
	}
	return s.pipelines.GetRun(workspaceID, id)
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

func (s *Service) uniqueName(workspaceID uint, base string) (string, error) {
	return slug.Unique(base, "app", func(candidate string) (bool, error) {
		return s.apps.ExistsByName(workspaceID, candidate)
	})
}

// IDByUID resolves an application's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.apps.IDByUID(uid) }

// A deleted node otherwise leaves its apps reading "running" with no container behind them.
func (s *Service) MarkNodeRemoved(serverID uint) {
	if serverID == 0 {
		return
	}
	apps, err := s.apps.ListByServer(serverID)
	if err != nil {
		logger.Warn("node removed: could not list applications", "server", serverID, "error", err)
		return
	}
	for i := range apps {
		app := apps[i]
		if app.Status == models.AppStatusStopped {
			continue
		}
		if err := s.apps.SetStatus(app.ID, models.AppStatusStopped); err != nil {
			logger.Warn("node removed: could not mark application stopped",
				"server", serverID, "app", app.ID, "error", err)
			continue
		}
		logger.Info("application marked stopped: its node was removed",
			"server", serverID, "app", app.ID, "name", app.Name, "workspace", app.WorkspaceID)
	}
}
