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

package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/datavolume"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/hostmount"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/placement"
	"github.com/miabi-io/miabi/internal/worker"
)

type ApplicationHandler struct {
	svc      *application.Service
	bus      *eventbus.Bus
	audit    *audit.Logger
	ee       enterprise.EE
	logs     *logstore.Store
	placer   *Placer
	upgrader websocket.Upgrader
}

// Falls back to the DB tail when the store is disabled, the ref is empty, or the object is gone.
func (h *ApplicationHandler) SetLogStore(s *logstore.Store) { h.logs = s }

// SetPlacer wires location placement for app creates.
func (h *ApplicationHandler) SetPlacer(p *Placer) { h.placer = p }

func NewApplicationHandler(svc *application.Service, bus *eventbus.Bus, auditLog *audit.Logger, ee enterprise.EE) *ApplicationHandler {
	return &ApplicationHandler{
		svc:   svc,
		bus:   bus,
		audit: auditLog,
		ee:    ee,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			// Rejects cross-site Origins to prevent cross-site WebSocket hijacking.
			CheckOrigin: allowWSOrigin(nil),
		},
	}
}

// A "*" entry disables the origin check (dev).
func (h *ApplicationHandler) SetAllowedOrigins(origins []string) {
	h.upgrader.CheckOrigin = allowWSOrigin(origins)
}

// PortSpecBody is a container port declaration in app create/update requests.
type PortSpecBody struct {
	ContainerPort int    `json:"container_port" min:"1" max:"65535"`
	Protocol      string `json:"protocol" enum:"tcp,udp"`
	Scheme        string `json:"scheme" enum:"http,https"`
	Name          string `json:"name"`
}

type CreateAppRequest struct {
	Body struct {
		// Name is derived from DisplayName when blank.
		DisplayName string `json:"display_name" required:"true"`
		Name        string `json:"name"`
		// Location is where the app runs; empty uses the workspace's default location.
		Location string `json:"location"`
		// ServerID pins a node; platform admins only.
		ServerID   uint   `json:"server_id"`
		SourceType string `json:"source_type" enum:"image,git"`
		Image      string `json:"image"`
		Tag        string `json:"tag"`
		GitRepo    string `json:"git_repo"`
		GitRef     string `json:"git_ref"`
		// Rejected for image apps.
		BuildMethod string            `json:"build_method" enum:"auto,dockerfile,buildpack"`
		Builder     string            `json:"builder"`
		Buildpacks  []string          `json:"buildpacks"`
		BuildEnv    map[string]string `json:"build_env"`
		// Git source only; a repository with no pipeline still creates the app, which then builds directly.
		UsePipeline     bool           `json:"use_pipeline"`
		RegistryID      *uint          `json:"registry_id"`
		GitRepositoryID *uint          `json:"git_repository_id"`
		StackID         *uint          `json:"stack_id"`
		NetworkIDs      []uint         `json:"network_ids"`
		Ports           []PortSpecBody `json:"ports"`
		Command         []string       `json:"command"`
		Port            int            `json:"port"`
		MemoryBytes     int64          `json:"memory_bytes" min:"0"`
		NanoCPUs        int64          `json:"nano_cpus" min:"0"`
		// GPUCount requests whole devices (0 = none); gated by the AllowGPU plan capability.
		GPUCount int    `json:"gpu_count" min:"0"`
		GPUKind  string `json:"gpu_kind"`
		// Like docker run --user; a restricted-profile workspace must give a non-root numeric uid.
		RunAsUser string `json:"run_as_user"`
		// Allow-listed kernel privileges; need a privileged workspace.
		AddCapabilities []string `json:"add_capabilities"`
		Devices         []string `json:"devices"`
		// drop_capabilities takes any Linux capability, or ALL.
		ReadOnlyRootFilesystem bool     `json:"read_only_root_filesystem"`
		NoNewPrivileges        bool     `json:"no_new_privileges"`
		DropCapabilities       []string `json:"drop_capabilities"`
		RestartPolicy          string   `json:"restart_policy" enum:"no,always,unless-stopped,on-failure"`
		ImagePullPolicy        string   `json:"image_pull_policy" enum:"always,if-not-present,never"`
		// Omitted means container, which Okapi fills in so the stored kind is never empty.
		RuntimeKind          string                   `json:"runtime_kind" enum:"container,service" default:"container"`
		Replicas             int                      `json:"replicas" min:"0" max:"100"`
		PlacementConstraints []string                 `json:"placement_constraints"`
		UpdateConfig         *ServiceUpdateConfigBody `json:"update_config"`
		// Metadata: user labels. Reserved "miabi.io/" keys are stripped.
		Metadata map[string]string `json:"metadata"`
	} `json:"body"`
}

// ServiceUpdateConfigBody tunes the Swarm rolling update for a service app.
type ServiceUpdateConfigBody struct {
	Parallelism  int `json:"parallelism"`
	DelaySeconds int `json:"delay_seconds"`
}

func (b *ServiceUpdateConfigBody) toModel() *models.ServiceUpdateConfig {
	if b == nil {
		return nil
	}
	return &models.ServiceUpdateConfig{Parallelism: b.Parallelism, DelaySeconds: b.DelaySeconds}
}

type UpdateAppRequest struct {
	Body UpdateAppBody `json:"body"`
}

type UpdateAppBody struct {
	DisplayName                   string                   `json:"display_name"`
	Image                         string                   `json:"image"`
	Tag                           string                   `json:"tag"`
	GitRepo                       string                   `json:"git_repo"`
	GitRef                        string                   `json:"git_ref"`
	RegistryID                    *uint                    `json:"registry_id"`
	GitRepositoryID               *uint                    `json:"git_repository_id"`
	StackID                       *uint                    `json:"stack_id"`
	NetworkIDs                    []uint                   `json:"network_ids"`
	Ports                         []PortSpecBody           `json:"ports"`
	Command                       []string                 `json:"command"`
	Port                          int                      `json:"port"`
	MemoryBytes                   int64                    `json:"memory_bytes" min:"0"`
	NanoCPUs                      int64                    `json:"nano_cpus" min:"0"`
	GPUCount                      int                      `json:"gpu_count" min:"0"`
	GPUKind                       string                   `json:"gpu_kind"`
	RunAsUser                     string                   `json:"run_as_user"`
	AddCapabilities               []string                 `json:"add_capabilities"`
	Devices                       []string                 `json:"devices"`
	ReadOnlyRootFilesystem        *bool                    `json:"read_only_root_filesystem"`
	NoNewPrivileges               *bool                    `json:"no_new_privileges"`
	DropCapabilities              []string                 `json:"drop_capabilities"`
	RestartPolicy                 string                   `json:"restart_policy" enum:"no,always,unless-stopped,on-failure"`
	ImagePullPolicy               string                   `json:"image_pull_policy" enum:"always,if-not-present,never"`
	RuntimeKind                   string                   `json:"runtime_kind" enum:"container,service"`
	Replicas                      int                      `json:"replicas" min:"0" max:"100"`
	PlacementConstraints          []string                 `json:"placement_constraints"`
	UpdateConfig                  *ServiceUpdateConfigBody `json:"update_config"`
	BuildMethod                   string                   `json:"build_method" enum:"auto,dockerfile,buildpack"`
	Builder                       string                   `json:"builder"`
	Buildpacks                    []string                 `json:"buildpacks"`
	BuildEnv                      map[string]string        `json:"build_env"`
	Metadata                      map[string]string        `json:"metadata"`
	ContainerLabels               map[string]string        `json:"container_labels"`
	DeployStrategy                string                   `json:"deploy_strategy" enum:"recreate,rolling,canary"`
	ReconcilePolicy               string                   `json:"reconcile_policy" enum:"inherit,off,observe,enforce"`
	CanaryInitialWeight           int                      `json:"canary_initial_weight"`
	CanaryStepWeight              int                      `json:"canary_step_weight"`
	CanaryStepIntervalSeconds     int                      `json:"canary_step_interval_seconds"`
	HealthcheckType               string                   `json:"healthcheck_type" enum:"image,none,http,command"`
	HealthcheckHTTPPath           string                   `json:"healthcheck_http_path"`
	HealthcheckPort               int                      `json:"healthcheck_port"`
	HealthcheckCommand            string                   `json:"healthcheck_command"`
	HealthcheckIntervalSeconds    int                      `json:"healthcheck_interval_seconds"`
	HealthcheckTimeoutSeconds     int                      `json:"healthcheck_timeout_seconds"`
	HealthcheckRetries            int                      `json:"healthcheck_retries"`
	HealthcheckStartPeriodSeconds int                      `json:"healthcheck_start_period_seconds"`

	present map[string]bool
}

// UnmarshalJSON records which keys the client sent, so an omitted field is left alone.
func (b *UpdateAppBody) UnmarshalJSON(data []byte) error {
	type plain UpdateAppBody
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	*b = UpdateAppBody(p)
	b.present = make(map[string]bool, len(keys))
	for k := range keys {
		b.present[k] = true
	}
	return nil
}

// sent reports whether the client included key. A body not decoded from JSON counts every key as sent.
func (b *UpdateAppBody) sent(key string) bool {
	return b.present == nil || b.present[key]
}

// Compares what would be written, so a sent empty tag (which clears it) counts as a change.
func (b *UpdateAppBody) changesSource(app *models.Application) bool {
	changed := (b.Image != "" && b.Image != app.Image) || (b.sent("tag") && b.Tag != app.Tag)
	if app.SourceType == models.AppSourceGit {
		changed = changed ||
			(b.sent("git_repo") && strings.TrimSpace(b.GitRepo) != app.GitRepo) ||
			(b.sent("git_ref") && strings.TrimSpace(b.GitRef) != app.GitRef)
	}
	return changed
}

// applyTo writes the update's fields onto app. Fields the client left out keep their stored value.
func (b *UpdateAppBody) applyTo(app *models.Application) {
	if b.Image != "" {
		app.Image = b.Image
	}
	if b.sent("tag") {
		app.Tag = b.Tag
	}
	if b.Command != nil {
		app.Command = b.Command
	}
	if b.Metadata != nil {
		// Merge user labels, protecting built-in "miabi.io/" keys.
		app.Metadata = models.MergeUserMetadata(app.Metadata, b.Metadata)
	}
	if b.sent("port") {
		app.Port = b.Port
	}
	if b.sent("memory_bytes") {
		app.MemoryBytes = b.MemoryBytes
	}
	if b.sent("nano_cpus") {
		app.NanoCPUs = b.NanoCPUs
	}
	if b.sent("gpu_count") {
		app.GPUCount = b.GPUCount
	}
	if b.sent("gpu_kind") {
		app.GPUKind = b.GPUKind
	}
	if b.sent("run_as_user") {
		app.RunAsUser = b.RunAsUser // validated against the security profile in the service
	}
	if b.sent("add_capabilities") {
		app.AddCapabilities = b.AddCapabilities // allow-listed + gated in the service
	}
	if b.sent("devices") {
		app.Devices = b.Devices
	}
	if b.ReadOnlyRootFilesystem != nil {
		app.ReadOnlyRootFilesystem = *b.ReadOnlyRootFilesystem
	}
	if b.NoNewPrivileges != nil {
		app.NoNewPrivileges = *b.NoNewPrivileges
	}
	if b.DropCapabilities != nil {
		app.DropCapabilities = b.DropCapabilities
	}
	if b.RestartPolicy != "" {
		app.RestartPolicy = models.RestartPolicy(b.RestartPolicy)
	}
	if b.ImagePullPolicy != "" {
		app.ImagePullPolicy = models.ImagePullPolicy(b.ImagePullPolicy)
	}
	// Cluster runtime: empty kind / non-positive replicas leave the stored value.
	if b.RuntimeKind != "" {
		app.RuntimeKind = models.RuntimeKind(b.RuntimeKind)
	}
	if b.Replicas > 0 {
		app.Replicas = b.Replicas
	}
	if b.PlacementConstraints != nil {
		app.PlacementConstraints = b.PlacementConstraints
	}
	if b.UpdateConfig != nil {
		app.UpdateConfig = b.UpdateConfig.toModel()
	}
	if b.sent("registry_id") {
		app.RegistryID = b.RegistryID
	}
	if b.sent("git_repository_id") {
		app.GitRepositoryID = b.GitRepositoryID
	}
	if b.sent("stack_id") {
		app.StackID = b.StackID
	}
	// An empty git_repo is valid when a repository is attached.
	if app.SourceType == models.AppSourceGit {
		if b.sent("git_repo") {
			app.GitRepo = strings.TrimSpace(b.GitRepo)
		}
		if b.sent("git_ref") {
			app.GitRef = strings.TrimSpace(b.GitRef)
		}
		if b.BuildMethod != "" {
			app.BuildMethod = models.AppBuildMethod(b.BuildMethod)
		}
		if b.sent("builder") {
			app.Builder = b.Builder
		}
		if b.sent("buildpacks") {
			app.Buildpacks = b.Buildpacks
		}
		if b.sent("build_env") {
			app.BuildEnv = b.BuildEnv
		}
	}
	if p := models.ReconcilePolicy(b.ReconcilePolicy); p != "" && models.ValidReconcilePolicy(p) {
		app.ReconcilePolicy = p
	}
	if b.DeployStrategy != "" {
		app.DeployStrategy = models.DeployStrategy(b.DeployStrategy)
	}
	if b.CanaryInitialWeight > 0 {
		app.CanaryInitialWeight = b.CanaryInitialWeight
	}
	if b.CanaryStepWeight > 0 {
		app.CanaryStepWeight = b.CanaryStepWeight
	}
	if b.CanaryStepIntervalSeconds > 0 {
		app.CanaryStepIntervalSeconds = b.CanaryStepIntervalSeconds
	}
	if b.HealthcheckType != "" {
		app.HealthcheckType = models.HealthcheckType(b.HealthcheckType)
	}
	if b.sent("healthcheck_http_path") {
		app.HealthcheckHTTPPath = b.HealthcheckHTTPPath
	}
	if b.sent("healthcheck_port") {
		app.HealthcheckPort = b.HealthcheckPort
	}
	if b.sent("healthcheck_command") {
		app.HealthcheckCommand = b.HealthcheckCommand
	}
	if b.HealthcheckIntervalSeconds > 0 {
		app.HealthcheckIntervalSeconds = b.HealthcheckIntervalSeconds
	}
	if b.HealthcheckTimeoutSeconds > 0 {
		app.HealthcheckTimeoutSeconds = b.HealthcheckTimeoutSeconds
	}
	if b.HealthcheckRetries > 0 {
		app.HealthcheckRetries = b.HealthcheckRetries
	}
	if b.sent("healthcheck_start_period_seconds") && b.HealthcheckStartPeriodSeconds >= 0 {
		app.HealthcheckStartPeriodSeconds = b.HealthcheckStartPeriodSeconds
	}
}

func toPortSpecs(in []PortSpecBody) []application.PortSpec {
	out := make([]application.PortSpec, 0, len(in))
	for _, p := range in {
		out = append(out, application.PortSpec{ContainerPort: p.ContainerPort, Protocol: p.Protocol, Scheme: p.Scheme, Name: p.Name})
	}
	return out
}

type DeployRequest struct {
	// Max 900 seconds; 0 returns at once.
	Wait int `query:"wait"`
	Body struct {
		RegistryID *uint  `json:"registry_id"`
		Tag        string `json:"tag"`
		Strategy   string `json:"strategy" enum:"recreate,rolling,canary"`
		// NoCache rebuilds the image from scratch for this deploy only (git-source apps).
		NoCache bool `json:"no_cache"`
	} `json:"body"`
}

type PinReleaseRequest struct {
	Body struct {
		Pinned bool `json:"pinned"`
	} `json:"body"`
}

type SetEnvVarRequest struct {
	Body struct {
		Key      string `json:"key" required:"true"`
		Value    string `json:"value"`
		IsSecret bool   `json:"is_secret"`
	} `json:"body"`
}

type ImportEnvVarsRequest struct {
	Body struct {
		// Content is .env-style text (KEY=VALUE per line).
		Content string `json:"content" required:"true"`
		// IsSecret marks all imported variables as secrets (encrypted at rest).
		IsSecret bool `json:"is_secret"`
	} `json:"body"`
}

type RollbackRequest struct {
	// Wait blocks up to this many seconds (max 900) for the rollback to settle; see DeployRequest.
	Wait int `query:"wait"`
	Body struct {
		ReleaseID uint `json:"release_id" required:"true"`
	} `json:"body"`
}

type AttachVolumeRequest struct {
	Body struct {
		VolumeID uint   `json:"volume_id" required:"true"`
		Path     string `json:"path" required:"true"`
	} `json:"body"`
}

func (h *ApplicationHandler) Create(c *okapi.Context, req *CreateAppRequest) error {
	wsID := middlewares.WorkspaceID(c)
	placed, err := h.placer.place(c, placement.Request{
		Location: req.Body.Location, ServerID: req.Body.ServerID,
		Service: req.Body.RuntimeKind == string(models.RuntimeService),
	})
	if err != nil {
		if a := placementAbort(c, err); a != nil {
			return a
		}
		return h.mapErr(c, err)
	}
	app, err := h.svc.Create(wsID, application.CreateInput{
		DisplayName: req.Body.DisplayName, Handle: req.Body.Name, ServerID: placed.ServerID, SourceType: models.AppSourceType(req.Body.SourceType),
		Image: req.Body.Image, Tag: req.Body.Tag, GitRepo: req.Body.GitRepo, GitRef: req.Body.GitRef,
		BuildMethod: models.AppBuildMethod(req.Body.BuildMethod), Builder: req.Body.Builder,
		Buildpacks: req.Body.Buildpacks, BuildEnv: req.Body.BuildEnv,
		RegistryID: req.Body.RegistryID, GitRepositoryID: req.Body.GitRepositoryID,
		UsePipeline: req.Body.UsePipeline, UserID: userIDPtr(c),
		StackID:    req.Body.StackID,
		NetworkIDs: req.Body.NetworkIDs, Ports: toPortSpecs(req.Body.Ports),
		Command: req.Body.Command, Port: req.Body.Port,
		MemoryBytes: req.Body.MemoryBytes, NanoCPUs: req.Body.NanoCPUs,
		GPUCount: req.Body.GPUCount, GPUKind: req.Body.GPUKind,
		RunAsUser:              req.Body.RunAsUser,
		AddCapabilities:        req.Body.AddCapabilities,
		Devices:                req.Body.Devices,
		ReadOnlyRootFilesystem: req.Body.ReadOnlyRootFilesystem,
		NoNewPrivileges:        req.Body.NoNewPrivileges,
		DropCapabilities:       req.Body.DropCapabilities,
		RestartPolicy:          models.RestartPolicy(req.Body.RestartPolicy),
		ImagePullPolicy:        models.ImagePullPolicy(req.Body.ImagePullPolicy),
		RuntimeKind:            models.RuntimeKind(req.Body.RuntimeKind),
		Replicas:               req.Body.Replicas,
		PlacementConstraints:   req.Body.PlacementConstraints,
		UpdateConfig:           req.Body.UpdateConfig.toModel(),
		// Strip any reserved keys a client tries to set; Create stamps managed-by.
		Metadata: models.SanitizeUserMetadata(req.Body.Metadata),
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "app.create", app.ID)
	return created(c, app)
}

func (h *ApplicationHandler) List(c *okapi.Context) error {
	apps, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list applications", err)
	}
	return ok(c, apps)
}

// ResourceLimitsResponse advertises the platform per-app CPU/memory caps.
type ResourceLimitsResponse struct {
	MaxCPUCores int `json:"max_cpu_cores"` // 0 = unlimited
	MaxMemoryMB int `json:"max_memory_mb"` // 0 = unlimited
}

func (h *ApplicationHandler) ResourceLimits(c *okapi.Context) error {
	cores, mem := h.svc.ResourceLimits()
	return ok(c, ResourceLimitsResponse{MaxCPUCores: cores, MaxMemoryMB: mem})
}

func (h *ApplicationHandler) Get(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	return ok(c, app)
}

// Inspected from Docker rather than the stored deploy-time status.
func (h *ApplicationHandler) Status(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	return ok(c, h.svc.LiveStatus(c.Request().Context(), app))
}

func (h *ApplicationHandler) Overview(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	return ok(c, h.svc.Overview(app))
}

func (h *ApplicationHandler) Update(c *okapi.Context, req *UpdateAppRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if req.Body.DisplayName != "" {
		app.DisplayName = req.Body.DisplayName
	}
	// Reject only an actual change: many clients PATCH the object back with its current image.
	if err := sourceUnchangedLock(app, req); err != nil {
		return c.AbortWithError(http.StatusConflict, err)
	}
	// Resending the stored runtime is fine; a change has to hand over between runtimes.
	if rk := models.RuntimeKind(req.Body.RuntimeKind); rk != "" && rk != app.RuntimeKind {
		return c.AbortWithError(http.StatusConflict, application.ErrRuntimeChangeViaSettings)
	}
	if req.Body.ContainerLabels != nil {
		// Sets app.ContainerLabels so the later Update save stays consistent.
		if err := h.svc.SetContainerLabels(app, req.Body.ContainerLabels); err != nil {
			return h.mapLabelErr(c, err)
		}
	}
	b := &req.Body
	b.applyTo(app)
	app.Stack = nil    // cleared so the association isn't re-saved; StackID drives it
	app.Networks = nil // managed separately via SetNetworks (avoid association save)
	app.Ports = nil    // managed separately via SetPorts
	if err := h.svc.Update(app); err != nil {
		return h.mapErr(c, err)
	}
	if b.sent("network_ids") {
		if err := h.svc.SetNetworks(app, b.NetworkIDs); err != nil {
			return c.AbortInternalServerError("failed to update networks", err)
		}
	}
	if b.sent("ports") {
		if err := h.svc.SetPorts(app, toPortSpecs(b.Ports)); err != nil {
			return c.AbortInternalServerError("failed to update ports", err)
		}
	}
	h.record(c, app.WorkspaceID, "app.update", app.ID)
	h.markRedeploy(c, app)
	return ok(c, app)
}

func (h *ApplicationHandler) Delete(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.Delete(c.Request().Context(), app); err != nil {
		if errors.Is(err, application.ErrAppRunning) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to delete application", err)
	}
	h.record(c, app.WorkspaceID, "app.delete", app.ID)
	return message(c, "application deleted")
}

func (h *ApplicationHandler) ListEnvVars(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	vars, err := h.svc.ListEnvVars(app.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list env vars", err)
	}
	return ok(c, vars)
}

func (h *ApplicationHandler) SetEnvVar(c *okapi.Context, req *SetEnvVarRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.SetEnvVar(app.ID, req.Body.Key, req.Body.Value, req.Body.IsSecret); err != nil {
		return c.AbortInternalServerError("failed to set env var", err)
	}
	h.record(c, app.WorkspaceID, "app.env_set", app.ID)
	return message(c, changeMsg("environment variable set", h.markRedeploy(c, app)))
}

// ImportEnvVars bulk-upserts env vars from a pasted .env block.
func (h *ApplicationHandler) ImportEnvVars(c *okapi.Context, req *ImportEnvVarsRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	n, err := h.svc.ImportEnvVars(app.ID, req.Body.Content, req.Body.IsSecret)
	if err != nil {
		return c.AbortInternalServerError("failed to import env vars", err)
	}
	h.record(c, app.WorkspaceID, "app.env_import", app.ID)
	redeployed := n > 0 && h.markRedeploy(c, app)
	return ok(c, map[string]any{"imported": n, "redeploying": redeployed})
}

// Reading a secret's plaintext is gated to workspace admins, mirroring the Secret Manager reveal.
func (h *ApplicationHandler) RevealEnvVar(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	key := c.Param("key")
	val, err := h.svc.RevealEnvVar(app.ID, key)
	if err != nil {
		if errors.Is(err, application.ErrEnvVarNotFound) {
			return c.AbortNotFound("environment variable not found")
		}
		return c.AbortInternalServerError("failed to reveal env var", err)
	}
	h.record(c, app.WorkspaceID, "app.env_reveal", app.ID)
	return ok(c, map[string]string{"key": key, "value": val})
}

func (h *ApplicationHandler) DeleteEnvVar(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.DeleteEnvVar(app.ID, c.Param("key")); err != nil {
		return c.AbortInternalServerError("failed to delete env var", err)
	}
	h.record(c, app.WorkspaceID, "app.env_delete", app.ID)
	return message(c, changeMsg("environment variable deleted", h.markRedeploy(c, app)))
}

type SetLabelsRequest struct {
	Body struct {
		Labels map[string]string `json:"labels"`
	} `json:"body"`
}

func (h *ApplicationHandler) ListLabels(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	labels := app.ContainerLabels
	if labels == nil {
		labels = map[string]string{}
	}
	return ok(c, labels)
}

// Changes apply on the next deploy, like ports, volumes or env vars.
func (h *ApplicationHandler) SetLabels(c *okapi.Context, req *SetLabelsRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.SetContainerLabels(app, req.Body.Labels); err != nil {
		return h.mapLabelErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.labels_update", app.ID)
	return message(c, changeMsg("labels updated", h.markRedeploy(c, app)))
}

func (h *ApplicationHandler) mapLabelErr(c *okapi.Context, err error) error {
	if a := quotaAbort(c, err); a != nil { // CAPABILITY_DENIED -> 403
		return a
	}
	switch {
	case errors.Is(err, application.ErrCustomLabelsDisabled):
		return c.AbortForbidden(err.Error(), err) // FEATURE_DISABLED
	case errors.Is(err, application.ErrLabelReserved),
		errors.Is(err, application.ErrTooManyLabels),
		errors.Is(err, application.ErrLabelInvalid):
		return c.AbortWithError(http.StatusUnprocessableEntity, err)
	default:
		return c.AbortInternalServerError("failed to update labels", err)
	}
}

type AttachConfigRequest struct {
	Body struct {
		ConfigID uint   `json:"config_id" required:"true"`
		Key      string `json:"key"`
		Path     string `json:"path" required:"true"`
		Mode     string `json:"mode"`
	} `json:"body"`
}

func (h *ApplicationHandler) AttachConfig(c *okapi.Context, req *AttachConfigRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.AttachConfig(app, req.Body.ConfigID, req.Body.Key, req.Body.Path, req.Body.Mode); err != nil {
		switch {
		case errors.Is(err, application.ErrConfigNotFound):
			return c.AbortNotFound(err.Error())
		case errors.Is(err, application.ErrConfigKeyNotFound), errors.Is(err, application.ErrMountPathRequired):
			return c.AbortBadRequest(err.Error())
		default:
			return c.AbortInternalServerError("failed to attach config", err)
		}
	}
	h.record(c, app.WorkspaceID, "app.config_attach", app.ID)
	h.markRedeploy(c, app)
	return ok(c, app)
}

func (h *ApplicationHandler) DetachConfig(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	configID, err := strconv.Atoi(c.Param("configID"))
	if err != nil || configID <= 0 {
		return c.AbortBadRequest("invalid config id")
	}
	if err := h.svc.DetachConfig(app, uint(configID), c.Query("key")); err != nil {
		if errors.Is(err, application.ErrMountNotFound) {
			return c.AbortNotFound(err.Error())
		}
		return c.AbortInternalServerError("failed to detach config", err)
	}
	h.record(c, app.WorkspaceID, "app.config_detach", app.ID)
	h.markRedeploy(c, app)
	return ok(c, app)
}

func (h *ApplicationHandler) AttachVolume(c *okapi.Context, req *AttachVolumeRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.AttachVolume(app, req.Body.VolumeID, req.Body.Path); err != nil {
		switch {
		case errors.Is(err, application.ErrVolumeNotFound):
			return c.AbortNotFound("volume not found")
		case errors.Is(err, application.ErrMountPathRequired):
			return c.AbortBadRequest("path is required")
		case errors.Is(err, application.ErrNodeMismatch), errors.Is(err, application.ErrLocalVolumeReplicated),
			errors.Is(err, application.ErrVolumeUnverifiable), errors.Is(err, application.ErrVolumeLocation):
			return c.AbortBadRequest(err.Error())
		default:
			return c.AbortInternalServerError("failed to attach volume", err)
		}
	}
	h.record(c, app.WorkspaceID, "app.volume_attach", app.ID)
	h.markRedeploy(c, app)
	return ok(c, app)
}

func (h *ApplicationHandler) DetachVolume(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	volumeID, err := strconv.Atoi(c.Param("volumeID"))
	if err != nil || volumeID <= 0 {
		return c.AbortBadRequest("invalid volume id")
	}
	if err := h.svc.DetachVolume(app, uint(volumeID)); err != nil {
		return c.AbortInternalServerError("failed to detach volume", err)
	}
	h.record(c, app.WorkspaceID, "app.volume_detach", app.ID)
	h.markRedeploy(c, app)
	return ok(c, app)
}

// AttachHostMountRequest is the body for attaching a privileged host bind.
type AttachHostMountRequest struct {
	Body struct {
		Preset   string `json:"preset" required:"true"`
		Path     string `json:"path"`
		ReadOnly bool   `json:"read_only"`
	} `json:"body"`
}

func (h *ApplicationHandler) AttachHostMount(c *okapi.Context, req *AttachHostMountRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.AttachHostMount(app, req.Body.Preset, req.Body.Path, req.Body.ReadOnly); err != nil {
		switch {
		case errors.Is(err, application.ErrUnknownHostPreset):
			return c.AbortBadRequest("unknown host mount preset")
		case errors.Is(err, application.ErrHostMountNotPrivileged):
			return c.AbortForbidden("host mounts require a privileged workspace")
		default:
			return c.AbortInternalServerError("failed to attach host mount", err)
		}
	}
	h.record(c, app.WorkspaceID, "app.host_mount_attach", app.ID)
	h.markRedeploy(c, app)
	return ok(c, app)
}

func (h *ApplicationHandler) DetachHostMount(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	preset := c.Param("preset")
	if preset == "" {
		return c.AbortBadRequest("preset is required")
	}
	if err := h.svc.DetachHostMount(app, preset); err != nil {
		return c.AbortInternalServerError("failed to detach host mount", err)
	}
	h.record(c, app.WorkspaceID, "app.host_mount_detach", app.ID)
	h.markRedeploy(c, app)
	return ok(c, app)
}

// HostMountPresets returns the allow-listed host bind presets (catalog for the UI).
func (h *ApplicationHandler) HostMountPresets(c *okapi.Context) error {
	return ok(c, hostmount.All())
}

// Config changes no longer auto-deploy; reports whether the flag was set to tailor the message.
func (h *ApplicationHandler) markRedeploy(c *okapi.Context, app *models.Application) bool {
	marked, err := h.svc.MarkRedeployRequired(app)
	if err != nil || !marked {
		return false
	}
	h.record(c, app.WorkspaceID, "app.redeploy_required", app.ID)
	return true
}

func changeMsg(base string, redeployRequired bool) string {
	if redeployRequired {
		return base + " — redeploy required"
	}
	return base
}

func (h *ApplicationHandler) Deploy(c *okapi.Context, req *DeployRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	res, err := h.svc.RequestDeploy(app, req.Body.RegistryID, req.Body.Tag, models.DeployStrategy(req.Body.Strategy), userIDPtr(c), req.Body.NoCache)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.deploy", app.ID)
	// A direct deploy still returns the bare Deployment, so existing clients see no change.
	if res.Run != nil {
		run := res.Run
		if req.Wait > 0 {
			var done bool
			if run, done = h.waitPipelineRun(c, app.WorkspaceID, run, req.Wait); !done {
				c.SetHeader(waitHeader, "timeout")
			}
		}
		return created(c, PipelineRunAccepted{Kind: "pipeline_run", Run: run})
	}
	dep := res.Deployment
	if req.Wait > 0 {
		var done bool
		if dep, done = h.waitDeployment(c, dep, req.Wait); !done {
			c.SetHeader(waitHeader, "timeout")
		}
	}
	return created(c, dep)
}

func (h *ApplicationHandler) InvalidateBuildCache(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.InvalidateBuildCache(app); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.build_cache.invalidate", app.ID)
	return message(c, "build cache invalidated — the next build will rebuild every layer")
}

type PipelineRunAccepted struct {
	Kind string              `json:"kind"` // always "pipeline_run"
	Run  *models.PipelineRun `json:"run"`
}

// Start / Stop / Restart act on the app's active release container.

func (h *ApplicationHandler) Start(c *okapi.Context) error {
	return h.lifecycle(c, "start")
}
func (h *ApplicationHandler) Stop(c *okapi.Context) error {
	return h.lifecycle(c, "stop")
}
func (h *ApplicationHandler) Restart(c *okapi.Context) error {
	return h.lifecycle(c, "restart")
}

func (h *ApplicationHandler) lifecycle(c *okapi.Context, action string) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	ctx := c.Request().Context()
	var dep *models.Deployment
	switch action {
	case "start":
		dep, err = h.svc.Start(ctx, app)
	case "stop":
		err = h.svc.Stop(ctx, app)
	case "restart":
		dep, err = h.svc.Restart(ctx, app)
	}
	if errors.Is(err, application.ErrNotDeployable) {
		return c.AbortWithError(409, errors.New("application has no running container; deploy it first"))
	}
	if errors.Is(err, datavolume.ErrLost) {
		return c.AbortWithError(409, err)
	}
	if err != nil {
		return c.AbortInternalServerError("failed to "+action+" application", err)
	}
	h.record(c, app.WorkspaceID, "app."+action, app.ID)
	// Stamped even when the action became a redeploy: the user asked for a restart, so that is what happened.
	h.svc.RecordLifecycle(app.ID, action, userIDPtr(c))
	if dep != nil {
		return created(c, dep)
	}
	return message(c, "application "+action+" requested")
}

// ChangeRuntimeRequest moves an app between the container and service runtimes.
type ChangeRuntimeRequest struct {
	Body struct {
		Runtime string `json:"runtime" required:"true" enum:"container,service"`
		// Replicas applies to the service runtime; omitted keeps the stored count.
		Replicas int `json:"replicas,omitempty" min:"0" max:"100"`
		// DryRun reports what the change would do without changing anything.
		DryRun bool `json:"dry_run,omitempty"`
	} `json:"body"`
}

// ChangeRuntimeResult is the preflight and, once applied, the deployment doing the handover.
type ChangeRuntimeResult struct {
	Preflight  application.RuntimePreflight `json:"preflight"`
	Deployment *models.Deployment           `json:"deployment,omitempty"`
}

// ChangeRuntime switches an app's runtime and redeploys it, or with dry_run only reports the preflight.
func (h *ApplicationHandler) ChangeRuntime(c *okapi.Context, req *ChangeRuntimeRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	ch := application.RuntimeChange{Runtime: models.RuntimeKind(req.Body.Runtime), Replicas: req.Body.Replicas}
	pre := h.svc.PreflightRuntime(app, ch)
	if req.Body.DryRun {
		return ok(c, ChangeRuntimeResult{Preflight: pre})
	}
	dep, err := h.svc.ChangeRuntime(app, ch, userIDPtr(c))
	if err != nil {
		if errors.Is(err, application.ErrRuntimeChangeBlocked) {
			return c.AbortWithError(http.StatusConflict, err)
		}
		return h.mapErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.runtime.change", app.ID)
	return ok(c, ChangeRuntimeResult{Preflight: pre, Deployment: dep})
}

// ScaleRequest sets a service app's replica count.
type ScaleRequest struct {
	Body struct {
		Replicas int `json:"replicas" required:"true" min:"1" max:"100"`
	} `json:"body"`
}

func (h *ApplicationHandler) Scale(c *okapi.Context, req *ScaleRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.Scale(c.Request().Context(), app, req.Body.Replicas); err != nil {
		if errors.Is(err, application.ErrNotService) {
			return c.AbortBadRequest("scaling is only available for service-runtime (cluster) applications")
		}
		return h.mapErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.scale", app.ID)
	return message(c, "application scaled")
}

func (h *ApplicationHandler) Rollback(c *okapi.Context, req *RollbackRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	dep, err := h.svc.Rollback(app, req.Body.ReleaseID, userIDPtr(c))
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	h.record(c, app.WorkspaceID, "app.rollback", app.ID)
	if req.Wait > 0 {
		var done bool
		if dep, done = h.waitDeployment(c, dep, req.Wait); !done {
			c.SetHeader(waitHeader, "timeout")
		}
	}
	return created(c, dep)
}

type StartCanaryRequest struct {
	Body struct {
		RegistryID *uint  `json:"registry_id"`
		Tag        string `json:"tag"`
	} `json:"body"`
}

type CanaryWeightRequest struct {
	Body struct {
		Weight int `json:"weight" required:"true"`
	} `json:"body"`
}

// CanaryMatchRuleBody is one request-attribute condition in a canary rule set.
type CanaryMatchRuleBody struct {
	Source   string `json:"source" enum:"header,query,cookie,ip" required:"true"`
	Name     string `json:"name"`
	Operator string `json:"operator" enum:"equals,not_equals,contains,not_contains,starts_with,ends_with,regex,in" required:"true"`
	Value    string `json:"value" required:"true"`
}

type CanaryRoutingRequest struct {
	Body struct {
		Mode      string                `json:"mode" enum:"auto,manual" required:"true"`
		Exclusive bool                  `json:"exclusive"`
		Priority  int                   `json:"priority"`
		Match     []CanaryMatchRuleBody `json:"match"`
	} `json:"body"`
}

// Warnings are advisory: the save happened, but something outside Miabi must hold for the rules to route.
type CanaryRoutingResponse struct {
	Message  string   `json:"message"`
	Warnings []string `json:"warnings,omitempty"`
}

type CanaryPreviewRequest struct {
	Body struct {
		Headers map[string]string `json:"headers"`
		Query   map[string]string `json:"query"`
		Cookies map[string]string `json:"cookies"`
		IP      string            `json:"ip"`
	} `json:"body"`
}

// StartCanary deploys a new version alongside the running release (canary).
func (h *ApplicationHandler) StartCanary(c *okapi.Context, req *StartCanaryRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	dep, err := h.svc.StartCanary(app, req.Body.RegistryID, req.Body.Tag)
	if err != nil {
		return h.mapCanaryErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.canary.start", app.ID)
	return created(c, dep)
}

func (h *ApplicationHandler) PauseCanary(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.PauseCanary(app); err != nil {
		return h.mapCanaryErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.canary.pause", app.ID)
	return message(c, "canary rollout paused")
}

func (h *ApplicationHandler) ResumeCanary(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.ResumeCanary(app); err != nil {
		return h.mapCanaryErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.canary.resume", app.ID)
	return message(c, "canary rollout resumed")
}

// SetCanaryWeight shifts the share of traffic going to the canary (no redeploy).
func (h *ApplicationHandler) SetCanaryWeight(c *okapi.Context, req *CanaryWeightRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.SetCanaryWeight(c.Request().Context(), app, req.Body.Weight); err != nil {
		return h.mapCanaryErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.canary.weight", app.ID)
	return message(c, "canary traffic updated")
}

// PromoteCanary makes the canary the new stable release.
func (h *ApplicationHandler) PromoteCanary(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	dep, err := h.svc.PromoteCanary(app, userIDPtr(c))
	if err != nil {
		return h.mapCanaryErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.canary.promote", app.ID)
	return created(c, dep)
}

// AbortCanary discards the canary and returns all traffic to stable.
func (h *ApplicationHandler) AbortCanary(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.svc.AbortCanary(c.Request().Context(), app); err != nil {
		return h.mapCanaryErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.canary.abort", app.ID)
	return message(c, "canary aborted")
}

func (h *ApplicationHandler) SetCanaryRouting(c *okapi.Context, req *CanaryRoutingRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	in := application.CanaryRouting{
		Mode:      models.CanaryMode(req.Body.Mode),
		Exclusive: req.Body.Exclusive,
		Priority:  req.Body.Priority,
	}
	for _, r := range req.Body.Match {
		in.Match = append(in.Match, models.CanaryMatchRule{Source: r.Source, Name: r.Name, Operator: r.Operator, Value: r.Value})
	}

	if advancedCanaryRequested(in) {
		if err := h.ee.RequireMutable(enterprise.FlagAdvancedCanary); err != nil {
			return entitlementAbort(c, err)
		}
	}
	warnings, err := h.svc.SetCanaryRouting(c.Request().Context(), app, in)
	if err != nil {
		return h.mapCanaryErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.canary.routing", app.ID)
	return ok(c, CanaryRoutingResponse{Message: "canary routing updated", Warnings: warnings})
}

func advancedCanaryRequested(in application.CanaryRouting) bool {
	return in.Mode == models.CanaryModeManual || len(in.Match) > 0 || in.Exclusive || in.Priority != 0
}

func (h *ApplicationHandler) PreviewCanaryRouting(c *okapi.Context, req *CanaryPreviewRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	return ok(c, application.PreviewCanaryRouting(app, application.CanaryPreviewRequest{
		Headers: req.Body.Headers,
		Query:   req.Body.Query,
		Cookies: req.Body.Cookies,
		IP:      req.Body.IP,
	}))
}

func (h *ApplicationHandler) mapCanaryErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, application.ErrNoCanary):
		return c.AbortWithError(409, err)
	case errors.Is(err, application.ErrCanaryActive):
		return c.AbortWithError(409, err)
	case errors.Is(err, application.ErrNotDeployable):
		return c.AbortWithError(409, errors.New("application has no running release; deploy it first"))
	case errors.Is(err, application.ErrReleaseNotFound):
		return c.AbortNotFound("release not found")
	case isCanaryConfigErr(err):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("canary operation failed", err)
	}
}

func isCanaryConfigErr(err error) bool {
	var re *application.ErrCanaryMatchRegex
	if errors.As(err, &re) {
		return true
	}
	for _, e := range []error{
		application.ErrCanaryModeInvalid, application.ErrCanaryRulesNeedManual,
		application.ErrCanaryExclusiveNoMatch, application.ErrCanaryPooledZeroWeight,
		application.ErrCanaryTooManyRules, application.ErrCanaryMatchSource,
		application.ErrCanaryMatchOperator, application.ErrCanaryMatchName,
		application.ErrCanaryMatchValue, application.ErrCanaryPriority,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func (h *ApplicationHandler) ListDeployments(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	deps, err := h.svc.ListDeployments(app.ID, 50)
	if err != nil {
		return c.AbortInternalServerError("failed to list deployments", err)
	}
	return ok(c, deps)
}

func (h *ApplicationHandler) ListReleases(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	rels, err := h.svc.ListReleases(app.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list releases", err)
	}
	return ok(c, rels)
}

// GetRelease returns a single release's detail.
func (h *ApplicationHandler) GetRelease(c *okapi.Context) error {
	app, rel, err := h.loadRelease(c)
	if err != nil {
		return err
	}
	_ = app
	return ok(c, rel)
}

// PinRelease toggles a release's pinned (deletion-protected) flag.
func (h *ApplicationHandler) PinRelease(c *okapi.Context, req *PinReleaseRequest) error {
	app, _, err := h.loadRelease(c)
	if err != nil {
		return err
	}
	relID, _ := strconv.Atoi(c.Param("releaseID"))
	rel, err := h.svc.SetReleasePinned(app, uint(relID), req.Body.Pinned)
	if err != nil {
		return h.mapReleaseErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.release_pin", app.ID)
	return ok(c, rel)
}

// DeleteRelease removes a non-active, non-pinned release.
func (h *ApplicationHandler) DeleteRelease(c *okapi.Context) error {
	app, rel, err := h.loadRelease(c)
	if err != nil {
		return err
	}
	if err := h.svc.DeleteRelease(c.Request().Context(), app, rel.ID); err != nil {
		return h.mapReleaseErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.release_delete", app.ID)
	return message(c, "release deleted")
}

func (h *ApplicationHandler) loadRelease(c *okapi.Context) (*models.Application, *models.Release, error) {
	app, err := h.load(c)
	if err != nil {
		return nil, nil, c.AbortNotFound("application not found")
	}
	relID, err := strconv.Atoi(c.Param("releaseID"))
	if err != nil || relID <= 0 {
		return nil, nil, c.AbortBadRequest("invalid release id")
	}
	rel, err := h.svc.GetRelease(app, uint(relID))
	if err != nil {
		return nil, nil, c.AbortNotFound("release not found")
	}
	return app, rel, nil
}

func (h *ApplicationHandler) mapReleaseErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, application.ErrReleaseNotFound):
		return c.AbortNotFound("release not found")
	case errors.Is(err, application.ErrReleaseActive), errors.Is(err, application.ErrReleasePinned):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("release operation failed", err)
	}
}

func (h *ApplicationHandler) DeploymentLogs(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	depID, err := strconv.Atoi(c.Param("deploymentID"))
	if err != nil || depID <= 0 {
		return c.AbortBadRequest("invalid deployment id")
	}
	dep, err := h.svc.GetDeployment(uint(depID))
	if err != nil || dep.ApplicationID != app.ID {
		return c.AbortNotFound("deployment not found")
	}

	// Subscribe before replaying history to avoid missing events in between.
	events, unsubscribe := h.bus.Subscribe(worker.DeployTopic(dep.ID))
	defer unsubscribe()

	// A store miss or in-progress deployment replays the bounded DB tail before streaming live.
	for _, line := range replayLogHistory(h.logs, dep.LogRef, dep.Logs) {
		_ = c.SSESendJSON(eventbus.Event{Type: "log", Data: line})
	}
	if dep.Status.IsTerminal() {
		_ = c.SSESendJSON(eventbus.Event{Type: "status", Data: string(dep.Status)})
		return nil
	}

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-events:
			if !ok {
				return nil
			}
			_ = c.SSESendJSON(e)
			if e.Type == "status" {
				if st, _ := e.Data.(string); models.DeploymentStatus(st).IsTerminal() {
					return nil
				}
			}
		}
	}
}

func (h *ApplicationHandler) DeploymentLogsDownload(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	depID, err := strconv.Atoi(c.Param("deploymentID"))
	if err != nil || depID <= 0 {
		return c.AbortBadRequest("invalid deployment id")
	}
	dep, err := h.svc.GetDeployment(uint(depID))
	if err != nil || dep.ApplicationID != app.ID {
		return c.AbortNotFound("deployment not found")
	}
	filename := "deployment-" + strconv.Itoa(dep.Number) + ".log"
	return streamLogDownload(c, h.logs, dep.LogRef, dep.Logs, filename)
}

type DeploymentLogHistory struct {
	Status    string   `json:"status"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

func (h *ApplicationHandler) DeploymentLogsHistory(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	depID, err := strconv.Atoi(c.Param("deploymentID"))
	if err != nil || depID <= 0 {
		return c.AbortBadRequest("invalid deployment id")
	}
	dep, err := h.svc.GetDeployment(uint(depID))
	if err != nil || dep.ApplicationID != app.ID {
		return c.AbortNotFound("deployment not found")
	}
	return ok(c, DeploymentLogHistory{
		Status:    string(dep.Status),
		Lines:     replayLogHistory(h.logs, dep.LogRef, dep.Logs),
		Truncated: dep.LogTruncated,
	})
}

func (h *ApplicationHandler) load(c *okapi.Context) (*models.Application, error) {
	id, err := appRef(c, h.svc.IDByUID)
	if err != nil {
		return nil, errors.New("invalid app id")
	}
	return h.svc.Get(middlewares.WorkspaceID(c), id)
}

func (h *ApplicationHandler) record(c *okapi.Context, wsID uint, action string, appID uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "application", TargetID: strconv.Itoa(int(appID)), IP: c.RealIP()})
}

// Compares against stored values so a no-op passes through untouched.
func sourceUnchangedLock(app *models.Application, req *UpdateAppRequest) error {
	if !req.Body.changesSource(app) {
		return nil
	}
	return sourceLock(app)
}

func (h *ApplicationHandler) mapErr(c *okapi.Context, err error) error {
	if a := quotaAbort(c, err); a != nil {
		return a
	}
	switch {
	case errors.Is(err, application.ErrImageNotPermitted):
		return c.AbortForbidden(err.Error())
	case errors.Is(err, application.ErrImageRequired), errors.Is(err, application.ErrGitRepoRequired),
		errors.Is(err, application.ErrBuildConfigOnImage), errors.Is(err, application.ErrInvalidBuildMethod),
		errors.Is(err, application.ErrInvalidGPUCount):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, application.ErrResourceCap), errors.Is(err, application.ErrClusterDisabled),
		errors.Is(err, application.ErrLocalVolumeReplicated), errors.Is(err, application.ErrVolumeUnverifiable),
		errors.Is(err, application.ErrTooManyReplicas), errors.Is(err, models.ErrDevicesOnService),
		errors.Is(err, application.ErrHostBindService),
		errors.Is(err, application.ErrPortRange), errors.Is(err, models.ErrRunAsUserInvalid),
		errors.Is(err, models.ErrRunAsUserRoot), errors.Is(err, models.ErrCapabilityNotLinux),
		errors.Is(err, models.ErrCapabilityConflict):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, application.ErrStackNotFound):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, application.ErrNodeMismatch), errors.Is(err, application.ErrVolumeNotFound),
		errors.Is(err, application.ErrMountPathRequired), errors.Is(err, application.ErrVolumeLocation),
		errors.Is(err, application.ErrStackLocation):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, nodes.ErrNodeOffline), errors.Is(err, node.ErrNodeCordoned), errors.Is(err, node.ErrNodeNotFound),
		errors.Is(err, datavolume.ErrLost):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("application operation failed", err)
	}
}
