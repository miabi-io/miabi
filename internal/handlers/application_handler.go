// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/hostmount"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/worker"
)

type ApplicationHandler struct {
	svc      *application.Service
	bus      *eventbus.Bus
	audit    *audit.Logger
	logs     *logstore.Store
	upgrader websocket.Upgrader
}

// SetLogStore wires the shared execution-log store so deployment log reads
// replay a finished deployment's full history from the store (falling back to
// the DB tail when the store is disabled, the ref is empty, or the object is
// gone). nil keeps DB-tail-only reads.
func (h *ApplicationHandler) SetLogStore(s *logstore.Store) { h.logs = s }

func NewApplicationHandler(svc *application.Service, bus *eventbus.Bus, auditLog *audit.Logger) *ApplicationHandler {
	return &ApplicationHandler{
		svc:   svc,
		bus:   bus,
		audit: auditLog,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			// Browser clients (exec/log terminals): reject cross-site Origins to
			// prevent cross-site WebSocket hijacking. Defaults to same-origin only
			// until SetAllowedOrigins wires the configured allowlist.
			CheckOrigin: allowWSOrigin(nil),
		},
	}
}

// SetAllowedOrigins restricts WebSocket upgrades to same-origin plus the given
// browser origins (the CORS allowlist + web UI URL). A "*" entry disables the
// check (dev).
func (h *ApplicationHandler) SetAllowedOrigins(origins []string) {
	h.upgrader.CheckOrigin = allowWSOrigin(origins)
}

// --- DTOs ---

// PortSpecBody is a container port declaration in app create/update requests.
type PortSpecBody struct {
	ContainerPort int    `json:"container_port" min:"1" max:"65535"`
	Protocol      string `json:"protocol" enum:"tcp,udp"`
	Scheme        string `json:"scheme" enum:"http,https"`
	Name          string `json:"name"`
}

type CreateAppRequest struct {
	Body struct {
		// DisplayName is the free-text label. Name (optional) is the desired unique
		// slug handle; derived from DisplayName when blank.
		DisplayName string `json:"display_name" required:"true"`
		Name        string `json:"name"`
		ServerID    uint   `json:"server_id"` // node to place on (0 = local)
		SourceType  string `json:"source_type" enum:"image,git"`
		Image       string `json:"image"`
		Tag         string `json:"tag"`
		GitRepo     string `json:"git_repo"`
		GitRef      string `json:"git_ref"`
		// Build config (git source). BuildMethod: auto (default) | dockerfile |
		// buildpack. Builder optionally overrides the buildpack builder image;
		// Buildpacks/BuildEnv tune a buildpack build. Rejected for image apps.
		BuildMethod     string            `json:"build_method" enum:"auto,dockerfile,buildpack"`
		Builder         string            `json:"builder"`
		Buildpacks      []string          `json:"buildpacks"`
		BuildEnv        map[string]string `json:"build_env"`
		RegistryID      *uint             `json:"registry_id"`
		GitRepositoryID *uint             `json:"git_repository_id"`
		StackID         *uint             `json:"stack_id"`
		NetworkIDs      []uint            `json:"network_ids"`
		Ports           []PortSpecBody    `json:"ports"`
		Command         []string          `json:"command"`
		Port            int               `json:"port"`
		MemoryBytes     int64             `json:"memory_bytes" min:"0"`
		NanoCPUs        int64             `json:"nano_cpus" min:"0"`
		// GPUCount requests whole GPU devices (0 = none); GPUKind narrows to a
		// vendor/model. Gated by the AllowGPU plan capability.
		GPUCount        int               `json:"gpu_count" min:"0"`
		GPUKind         string            `json:"gpu_kind"`
		RestartPolicy   string            `json:"restart_policy" enum:"no,always,unless-stopped,on-failure"`
		ImagePullPolicy string            `json:"image_pull_policy" enum:"always,if-not-present,never"`
		// Cluster runtime (cluster mode). runtime_kind defaults to container;
		// "service" runs the app as a replicated Swarm service.
		RuntimeKind          string                   `json:"runtime_kind" enum:"container,service"`
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
	Body struct {
		// Name (optional) renames the unique slug handle; DisplayName edits the label.
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Image       string `json:"image"`
		Tag         string `json:"tag"`
		// Git source (git apps). GitRepo is the clone URL — leave empty to use the
		// attached repository's URL; GitRef is the branch/ref.
		GitRepo         string         `json:"git_repo"`
		GitRef          string         `json:"git_ref"`
		RegistryID      *uint          `json:"registry_id"`
		GitRepositoryID *uint          `json:"git_repository_id"`
		StackID         *uint          `json:"stack_id"`
		NetworkIDs      []uint         `json:"network_ids"`
		Ports           []PortSpecBody `json:"ports"`
		Command         []string       `json:"command"`
		Port            int            `json:"port"`
		MemoryBytes     int64          `json:"memory_bytes" min:"0"`
		NanoCPUs        int64          `json:"nano_cpus" min:"0"`
		// GPUCount requests whole GPU devices (0 = none); GPUKind narrows to a
		// vendor/model. Gated by the AllowGPU plan capability.
		GPUCount        int            `json:"gpu_count" min:"0"`
		GPUKind         string         `json:"gpu_kind"`
		RestartPolicy   string         `json:"restart_policy" enum:"no,always,unless-stopped,on-failure"`
		ImagePullPolicy string         `json:"image_pull_policy" enum:"always,if-not-present,never"`
		// Cluster runtime. Empty runtime_kind leaves the stored kind unchanged;
		// replicas <= 0 leaves it unchanged.
		RuntimeKind          string                   `json:"runtime_kind" enum:"container,service"`
		Replicas             int                      `json:"replicas" min:"0" max:"100"`
		PlacementConstraints []string                 `json:"placement_constraints"`
		UpdateConfig         *ServiceUpdateConfigBody `json:"update_config"`
		// Build config (git source). See CreateAppRequest. Empty build_method
		// leaves the stored method unchanged.
		BuildMethod string            `json:"build_method" enum:"auto,dockerfile,buildpack"`
		Builder     string            `json:"builder"`
		Buildpacks  []string          `json:"buildpacks"`
		BuildEnv    map[string]string `json:"build_env"`
		// Metadata: user labels merged over the app's current metadata; reserved
		// "miabi.io/" (built-in) keys are protected and cannot be changed here.
		Metadata map[string]string `json:"metadata"`
		// ContainerLabels: user-defined Docker labels stamped on the app's
		// container(s) (Traefik &c.). Gated by the AllowCustomLabels plan capability
		// + global kill-switch; reserved keys (io.miabi.*, com.docker.*) are
		// rejected. nil = leave unchanged; a redeploy applies changes.
		ContainerLabels map[string]string `json:"container_labels"`
		// Deployment strategy and canary tuning.
		DeployStrategy            string `json:"deploy_strategy" enum:"recreate,rolling,canary"`
		CanaryInitialWeight       int    `json:"canary_initial_weight"`
		CanaryStepWeight          int    `json:"canary_step_weight"`
		CanaryStepIntervalSeconds int    `json:"canary_step_interval_seconds"`
		// Healthcheck.
		HealthcheckType               string `json:"healthcheck_type" enum:"none,http,command"`
		HealthcheckHTTPPath           string `json:"healthcheck_http_path"`
		HealthcheckPort               int    `json:"healthcheck_port"`
		HealthcheckCommand            string `json:"healthcheck_command"`
		HealthcheckIntervalSeconds    int    `json:"healthcheck_interval_seconds"`
		HealthcheckTimeoutSeconds     int    `json:"healthcheck_timeout_seconds"`
		HealthcheckRetries            int    `json:"healthcheck_retries"`
		HealthcheckStartPeriodSeconds int    `json:"healthcheck_start_period_seconds"`
	} `json:"body"`
}

// toPortSpecs maps request port bodies to service PortSpecs.
func toPortSpecs(in []PortSpecBody) []application.PortSpec {
	out := make([]application.PortSpec, 0, len(in))
	for _, p := range in {
		out = append(out, application.PortSpec{ContainerPort: p.ContainerPort, Protocol: p.Protocol, Scheme: p.Scheme, Name: p.Name})
	}
	return out
}

type DeployRequest struct {
	Body struct {
		// RegistryID optionally overrides the app's registry credential for this
		// one deploy. Omit to use the app's configured credential.
		RegistryID *uint `json:"registry_id"`
		// Tag optionally deploys a specific image tag (image source) for this
		// one deploy. Omit to use the app's configured tag.
		Tag string `json:"tag"`
		// Strategy overrides the app's default rollout method for this deploy.
		// Omit to use the app's configured default.
		Strategy string `json:"strategy" enum:"recreate,rolling,canary"`
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

// --- App CRUD ---

func (h *ApplicationHandler) Create(c *okapi.Context, req *CreateAppRequest) error {
	wsID := middlewares.WorkspaceID(c)
	app, err := h.svc.Create(wsID, application.CreateInput{
		DisplayName: req.Body.DisplayName, Handle: req.Body.Name, ServerID: req.Body.ServerID, SourceType: models.AppSourceType(req.Body.SourceType),
		Image: req.Body.Image, Tag: req.Body.Tag, GitRepo: req.Body.GitRepo, GitRef: req.Body.GitRef,
		BuildMethod: models.AppBuildMethod(req.Body.BuildMethod), Builder: req.Body.Builder,
		Buildpacks: req.Body.Buildpacks, BuildEnv: req.Body.BuildEnv,
		RegistryID: req.Body.RegistryID, GitRepositoryID: req.Body.GitRepositoryID,
		StackID:    req.Body.StackID,
		NetworkIDs: req.Body.NetworkIDs, Ports: toPortSpecs(req.Body.Ports),
		Command: req.Body.Command, Port: req.Body.Port,
		MemoryBytes: req.Body.MemoryBytes, NanoCPUs: req.Body.NanoCPUs,
		GPUCount: req.Body.GPUCount, GPUKind: req.Body.GPUKind,
		RestartPolicy:        models.RestartPolicy(req.Body.RestartPolicy),
		ImagePullPolicy:      models.ImagePullPolicy(req.Body.ImagePullPolicy),
		RuntimeKind:          models.RuntimeKind(req.Body.RuntimeKind),
		Replicas:             req.Body.Replicas,
		PlacementConstraints: req.Body.PlacementConstraints,
		UpdateConfig:         req.Body.UpdateConfig.toModel(),
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

// ResourceLimits returns the platform-configured per-app resource caps so the UI
// can show them as hints and pre-validate.
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

// Status returns the live container status (and a stats snapshot) for the app,
// inspected from Docker rather than the stored deploy-time status.
func (h *ApplicationHandler) Status(c *okapi.Context) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	return ok(c, h.svc.LiveStatus(c.Request().Context(), app))
}

// Overview returns an aggregated summary (status, source, current release,
// resource counts) for the app detail page.
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
	if req.Body.Name != "" {
		if err := h.svc.SetName(app, req.Body.Name); err != nil {
			return h.mapErr(c, err)
		}
	}
	if req.Body.DisplayName != "" {
		app.DisplayName = req.Body.DisplayName
	}
	if req.Body.Image != "" {
		app.Image = req.Body.Image
	}
	app.Tag = req.Body.Tag
	if req.Body.Command != nil {
		app.Command = req.Body.Command
	}
	if req.Body.Metadata != nil {
		// Merge user labels, protecting built-in "miabi.io/" keys.
		app.Metadata = models.MergeUserMetadata(app.Metadata, req.Body.Metadata)
	}
	if req.Body.ContainerLabels != nil {
		// Gated + validated + persisted here (reserved keys rejected). Sets
		// app.ContainerLabels so the later Update save stays consistent.
		if err := h.svc.SetContainerLabels(app, req.Body.ContainerLabels); err != nil {
			return h.mapLabelErr(c, err)
		}
	}
	app.Port = req.Body.Port
	app.MemoryBytes = req.Body.MemoryBytes
	app.NanoCPUs = req.Body.NanoCPUs
	app.GPUCount = req.Body.GPUCount
	app.GPUKind = req.Body.GPUKind
	if req.Body.RestartPolicy != "" {
		app.RestartPolicy = models.RestartPolicy(req.Body.RestartPolicy)
	}
	if req.Body.ImagePullPolicy != "" {
		app.ImagePullPolicy = models.ImagePullPolicy(req.Body.ImagePullPolicy)
	}
	// Cluster runtime: empty kind / non-positive replicas leave the stored value.
	if req.Body.RuntimeKind != "" {
		app.RuntimeKind = models.RuntimeKind(req.Body.RuntimeKind)
	}
	if req.Body.Replicas > 0 {
		app.Replicas = req.Body.Replicas
	}
	if req.Body.PlacementConstraints != nil {
		app.PlacementConstraints = req.Body.PlacementConstraints
	}
	if req.Body.UpdateConfig != nil {
		app.UpdateConfig = req.Body.UpdateConfig.toModel()
	}
	app.RegistryID = req.Body.RegistryID
	app.GitRepositoryID = req.Body.GitRepositoryID
	app.StackID = req.Body.StackID
	// Git source + build config apply only to git apps; an empty build_method
	// leaves the stored method unchanged. The service validates/normalizes on
	// Update (an empty git_repo is valid when a repository is attached).
	if app.SourceType == models.AppSourceGit {
		app.GitRepo = strings.TrimSpace(req.Body.GitRepo)
		app.GitRef = strings.TrimSpace(req.Body.GitRef)
		if req.Body.BuildMethod != "" {
			app.BuildMethod = models.AppBuildMethod(req.Body.BuildMethod)
		}
		app.Builder = req.Body.Builder
		app.Buildpacks = req.Body.Buildpacks
		app.BuildEnv = req.Body.BuildEnv
	}
	if req.Body.DeployStrategy != "" {
		app.DeployStrategy = models.DeployStrategy(req.Body.DeployStrategy)
	}
	if req.Body.CanaryInitialWeight > 0 {
		app.CanaryInitialWeight = req.Body.CanaryInitialWeight
	}
	if req.Body.CanaryStepWeight > 0 {
		app.CanaryStepWeight = req.Body.CanaryStepWeight
	}
	if req.Body.CanaryStepIntervalSeconds > 0 {
		app.CanaryStepIntervalSeconds = req.Body.CanaryStepIntervalSeconds
	}
	if req.Body.HealthcheckType != "" {
		app.HealthcheckType = models.HealthcheckType(req.Body.HealthcheckType)
	}
	app.HealthcheckHTTPPath = req.Body.HealthcheckHTTPPath
	app.HealthcheckPort = req.Body.HealthcheckPort
	app.HealthcheckCommand = req.Body.HealthcheckCommand
	if req.Body.HealthcheckIntervalSeconds > 0 {
		app.HealthcheckIntervalSeconds = req.Body.HealthcheckIntervalSeconds
	}
	if req.Body.HealthcheckTimeoutSeconds > 0 {
		app.HealthcheckTimeoutSeconds = req.Body.HealthcheckTimeoutSeconds
	}
	if req.Body.HealthcheckRetries > 0 {
		app.HealthcheckRetries = req.Body.HealthcheckRetries
	}
	if req.Body.HealthcheckStartPeriodSeconds >= 0 && req.Body.HealthcheckType != "" {
		app.HealthcheckStartPeriodSeconds = req.Body.HealthcheckStartPeriodSeconds
	}
	app.Stack = nil    // cleared so the association isn't re-saved; StackID drives it
	app.Networks = nil // managed separately via SetNetworks (avoid association save)
	app.Ports = nil    // managed separately via SetPorts
	if err := h.svc.Update(app); err != nil {
		return h.mapErr(c, err)
	}
	if err := h.svc.SetNetworks(app, req.Body.NetworkIDs); err != nil {
		return c.AbortInternalServerError("failed to update networks", err)
	}
	if err := h.svc.SetPorts(app, toPortSpecs(req.Body.Ports)); err != nil {
		return c.AbortInternalServerError("failed to update ports", err)
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

// --- Env vars ---

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

// RevealEnvVar returns a single env var's decrypted value (Admin only, audited).
// Mirrors the Secret Manager reveal: list/set are lower-privileged, but reading
// a secret's plaintext is gated to workspace admins.
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

// --- Container labels (Traefik &c.) ---

// SetLabelsRequest replaces an app's user-defined Docker labels wholesale (the
// Detail page edits locally and PUTs the full set).
type SetLabelsRequest struct {
	Body struct {
		Labels map[string]string `json:"labels"`
	} `json:"body"`
}

// ListLabels returns the app's user-defined container labels (never the platform
// io.miabi.* labels, which are not user-managed).
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

// SetLabels replaces the app's user-defined container labels. Gated by the
// AllowCustomLabels plan capability + global kill-switch; reserved keys are
// rejected (422). Changes apply on the next deploy (redeploy-required), exactly
// like editing ports/volumes/env vars.
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

// mapLabelErr maps the gate + validation errors to their HTTP status, preserving
// the stable machine code carried by the coded errors.
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

// --- Volume mounts ---

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
		case errors.Is(err, application.ErrNodeMismatch), errors.Is(err, application.ErrLocalVolumeReplicated):
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

// markRedeploy flags a deployed app as needing a redeploy after a config change
// (config changes no longer auto-deploy). Best-effort; reports whether the flag
// was set so handlers can tailor their response message.
func (h *ApplicationHandler) markRedeploy(c *okapi.Context, app *models.Application) bool {
	marked, err := h.svc.MarkRedeployRequired(app)
	if err != nil || !marked {
		return false
	}
	h.record(c, app.WorkspaceID, "app.redeploy_required", app.ID)
	return true
}

// changeMsg appends a "redeploy required" note when a config change needs one.
func changeMsg(base string, redeployRequired bool) string {
	if redeployRequired {
		return base + " — redeploy required"
	}
	return base
}

// --- Deploy / rollback ---

func (h *ApplicationHandler) Deploy(c *okapi.Context, req *DeployRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	dep, err := h.svc.Deploy(app, req.Body.RegistryID, req.Body.Tag, models.DeployStrategy(req.Body.Strategy))
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, app.WorkspaceID, "app.deploy", app.ID)
	return created(c, dep)
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
	if err != nil {
		return c.AbortInternalServerError("failed to "+action+" application", err)
	}
	h.record(c, app.WorkspaceID, "app."+action, app.ID)
	// A start/restart that applied pending changes returns the deployment so the
	// client can follow its logs; a plain lifecycle action returns a message.
	if dep != nil {
		return created(c, dep)
	}
	return message(c, "application "+action+" requested")
}

// ScaleRequest sets a service app's replica count.
type ScaleRequest struct {
	Body struct {
		Replicas int `json:"replicas" required:"true" min:"1" max:"100"`
	} `json:"body"`
}

// Scale changes the replica count of a cluster (service) app, applied to the
// live Swarm service immediately.
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
	dep, err := h.svc.Rollback(app, req.Body.ReleaseID)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	h.record(c, app.WorkspaceID, "app.rollback", app.ID)
	return created(c, dep)
}

// --- Canary deployment ---

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
	dep, err := h.svc.PromoteCanary(app)
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
	default:
		return c.AbortInternalServerError("canary operation failed", err)
	}
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

// loadRelease resolves the app and release from the route, enforcing ownership.
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

// DeploymentLogs streams a deployment's build/deploy logs over SSE: the stored
// tail first, then live events until the deployment reaches a terminal state.
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

	// History: a finished deployment's full log is replayed from the store; an
	// in-progress one (or a store miss) replays the bounded DB tail, then streams
	// live from the bus — the unchanged SSE contract.
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

// DeploymentLogsDownload streams a deployment's full build/deploy log as a file
// download, workspace-scoped through the owning application.
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

// DeploymentLogHistory is the non-streaming logs payload for a (usually finished)
// deployment: the full stored log as lines plus the current status, so the UI can
// render a completed build with a single GET instead of opening an SSE stream.
type DeploymentLogHistory struct {
	Status    string   `json:"status"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

// DeploymentLogsHistory returns a deployment's full build/deploy log (from the
// store, else the bounded DB tail) as JSON — the load-once counterpart to the SSE
// stream, for viewing a finished deployment.
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

// --- helpers ---

func (h *ApplicationHandler) load(c *okapi.Context) (*models.Application, error) {
	id, err := resolveID(c.Param("appID"), h.svc.IDByUID)
	if err != nil {
		return nil, errors.New("invalid app id")
	}
	return h.svc.Get(middlewares.WorkspaceID(c), id)
}

func (h *ApplicationHandler) record(c *okapi.Context, wsID uint, action string, appID uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "application", TargetID: strconv.Itoa(int(appID)), IP: c.RealIP()})
}

func (h *ApplicationHandler) mapErr(c *okapi.Context, err error) error {
	if a := quotaAbort(c, err); a != nil {
		return a
	}
	switch {
	case errors.Is(err, application.ErrSlugTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, application.ErrNameInvalid):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, application.ErrImageRequired), errors.Is(err, application.ErrGitRepoRequired),
		errors.Is(err, application.ErrBuildConfigOnImage), errors.Is(err, application.ErrInvalidBuildMethod),
		errors.Is(err, application.ErrInvalidGPUCount):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, application.ErrResourceCap), errors.Is(err, application.ErrClusterDisabled),
		errors.Is(err, application.ErrLocalVolumeReplicated), errors.Is(err, application.ErrTooManyReplicas),
		errors.Is(err, application.ErrPortRange):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, application.ErrStackNotFound):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, application.ErrNodeMismatch), errors.Is(err, application.ErrVolumeNotFound),
		errors.Is(err, application.ErrMountPathRequired):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, nodes.ErrNodeOffline), errors.Is(err, node.ErrNodeCordoned), errors.Is(err, node.ErrNodeNotFound):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("application operation failed", err)
	}
}
