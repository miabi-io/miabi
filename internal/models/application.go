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

package models

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

// AppSourceType is how an application's image is obtained.
type AppSourceType string

const (
	AppSourceImage AppSourceType = "image" // pull a prebuilt image
	AppSourceGit   AppSourceType = "git"   // build from a Git repo
)

type AppBuildMethod string

const (
	// A Dockerfile in the tree builds with Docker, otherwise Cloud Native Buildpacks.
	BuildAuto AppBuildMethod = "auto"
	// BuildDockerfile always builds the repo's Dockerfile.
	BuildDockerfile AppBuildMethod = "dockerfile"
	BuildBuildpack  AppBuildMethod = "buildpack"
)

// ValidAppBuildMethod reports whether m is a known build method.
func ValidAppBuildMethod(m AppBuildMethod) bool {
	switch m {
	case BuildAuto, BuildDockerfile, BuildBuildpack:
		return true
	default:
		return false
	}
}

// DeployStrategy is how a new release replaces the running one.
type DeployStrategy string

const (
	// Brief downtime; use when two versions cannot run at once.
	DeployRecreate DeployStrategy = "recreate"
	DeployRolling  DeployStrategy = "rolling"
	DeployCanary   DeployStrategy = "canary"
)

// On the model because pipeline and manual deploys share no service; a CI canary once ran rolling.
func EffectiveDeployStrategy(app *Application, requested DeployStrategy) DeployStrategy {
	st := requested
	if !ValidDeployStrategy(st) {
		st = app.DeployStrategy
	}
	if !ValidDeployStrategy(st) {
		st = DeployRolling
	}
	// With nothing running there is nothing to split, so the first deploy is a straight rollout.
	if st == DeployCanary && app.CurrentReleaseID == nil {
		st = DeployRolling
	}
	return st
}

// ValidDeployStrategy reports whether s is a known strategy.
func ValidDeployStrategy(s DeployStrategy) bool {
	switch s {
	case DeployRecreate, DeployRolling, DeployCanary:
		return true
	default:
		return false
	}
}

// CanaryMode is who moves the canary's traffic weight during a rollout.
type CanaryMode string

const (
	// The only mode in Community.
	CanaryModeAuto   CanaryMode = "auto"
	CanaryModeManual CanaryMode = "manual"
)

// ValidCanaryMode reports whether m is a known canary mode.
func ValidCanaryMode(m CanaryMode) bool {
	switch m {
	case CanaryModeAuto, CanaryModeManual:
		return true
	default:
		return false
	}
}

// Canary match sources: where the gateway reads the value a rule compares.
const (
	CanaryMatchSourceHeader = "header"
	CanaryMatchSourceQuery  = "query"
	CanaryMatchSourceCookie = "cookie"
	CanaryMatchSourceIP     = "ip"
)

// Canary match operators
const (
	CanaryMatchOpEquals      = "equals"
	CanaryMatchOpNotEquals   = "not_equals"
	CanaryMatchOpContains    = "contains"
	CanaryMatchOpNotContains = "not_contains"
	CanaryMatchOpStartsWith  = "starts_with"
	CanaryMatchOpEndsWith    = "ends_with"
	CanaryMatchOpRegex       = "regex"
	CanaryMatchOpIn          = "in"
)

var (
	CanaryMatchSources = []string{
		CanaryMatchSourceHeader, CanaryMatchSourceQuery, CanaryMatchSourceCookie, CanaryMatchSourceIP,
	}
	CanaryMatchOperators = []string{
		CanaryMatchOpEquals, CanaryMatchOpNotEquals, CanaryMatchOpContains, CanaryMatchOpNotContains,
		CanaryMatchOpStartsWith, CanaryMatchOpEndsWith, CanaryMatchOpRegex, CanaryMatchOpIn,
	}
)

// ValidCanaryMatchSource reports whether s is a source the gateway can read.
func ValidCanaryMatchSource(s string) bool {
	for _, v := range CanaryMatchSources {
		if v == s {
			return true
		}
	}
	return false
}

// ValidCanaryMatchOperator reports whether op is a comparison the gateway implements.
func ValidCanaryMatchOperator(op string) bool {
	for _, v := range CanaryMatchOperators {
		if v == op {
			return true
		}
	}
	return false
}

// All rules must hold (AND), matching the gateway.
type CanaryMatchRule struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// A "service" app only deploys when cluster mode is enabled.
type RuntimeKind string

const (
	// RuntimeContainer runs the app as one plain Docker container on its node.
	RuntimeContainer RuntimeKind = "container"
	RuntimeService   RuntimeKind = "service"
)

// Prevents a request asking Swarm to schedule unbounded tasks (resource-exhaustion DoS).
const MaxServiceReplicas = 100

// Lets an operator exempt one app without switching the whole platform back to observing.
type ReconcilePolicy string

const (
	// ReconcileInherit follows the platform's control_manager_mode. The default.
	ReconcileInherit ReconcilePolicy = "inherit"
	// ReconcileOff leaves the app out of reconciliation entirely: not watched, not reported, not touched.
	ReconcileOff ReconcilePolicy = "off"
	// ReconcileObserve reports the app's drift but never acts on it, even while the platform enforces.
	ReconcileObserve ReconcilePolicy = "observe"
	// ReconcileEnforce lets the app be redeployed in place even while the platform only observes.
	ReconcileEnforce ReconcilePolicy = "enforce"
)

// ValidReconcilePolicy reports whether p is a known policy.
func ValidReconcilePolicy(p ReconcilePolicy) bool {
	switch p {
	case ReconcileInherit, ReconcileOff, ReconcileObserve, ReconcileEnforce:
		return true
	default:
		return false
	}
}

// ValidRuntimeKind reports whether k is a known runtime kind.
func ValidRuntimeKind(k RuntimeKind) bool {
	switch k {
	case RuntimeContainer, RuntimeService:
		return true
	default:
		return false
	}
}

// Transient, built on read from live Swarm task placement.
type NodePlacement struct {
	// A short swarm node id when the node is not yet correlated to a Miabi server.
	Name string `json:"name"`
	// Tasks is the number of the app's running tasks on this node.
	Tasks int `json:"tasks"`
}

type ServiceUpdateConfig struct {
	Parallelism  int `json:"parallelism,omitempty"`
	DelaySeconds int `json:"delay_seconds,omitempty"`
}

type RestartPolicy string

const (
	RestartNo            RestartPolicy = "no"
	RestartAlways        RestartPolicy = "always"
	RestartUnlessStopped RestartPolicy = "unless-stopped" // restart unless explicitly stopped (default)
	RestartOnFailure     RestartPolicy = "on-failure"     // restart only on non-zero exit
)

// ValidRestartPolicy reports whether p is a known restart policy.
func ValidRestartPolicy(p RestartPolicy) bool {
	switch p {
	case RestartNo, RestartAlways, RestartUnlessStopped, RestartOnFailure:
		return true
	default:
		return false
	}
}

// Digest-pinned refs are immutable, so they are never re-pulled when already present.
type ImagePullPolicy string

const (
	PullAlways       ImagePullPolicy = "always"         // always pull (default)
	PullIfNotPresent ImagePullPolicy = "if-not-present" // pull only when the image is absent locally
	PullNever        ImagePullPolicy = "never"          // never pull; fail if the image is absent
)

// ValidImagePullPolicy reports whether p is a known image pull policy.
func ValidImagePullPolicy(p ImagePullPolicy) bool {
	switch p {
	case PullAlways, PullIfNotPresent, PullNever:
		return true
	default:
		return false
	}
}

// HealthcheckType is how an application's container health is probed.
type HealthcheckType string

const (
	HealthcheckImage   HealthcheckType = "image"   // the image's own HEALTHCHECK, if it declares one
	HealthcheckNone    HealthcheckType = "none"    // no check at all, the image's HEALTHCHECK included
	HealthcheckHTTP    HealthcheckType = "http"    // HTTP GET against a path/port
	HealthcheckCommand HealthcheckType = "command" // shell command (CMD-SHELL)
)

// An image check does not gate: Docker probes only after a full interval.
func (t HealthcheckType) GatesDeploy() bool {
	return t == HealthcheckHTTP || t == HealthcheckCommand
}

// ValidHealthcheckType reports whether t is a known healthcheck type.
func ValidHealthcheckType(t HealthcheckType) bool {
	switch t {
	case HealthcheckImage, HealthcheckNone, HealthcheckHTTP, HealthcheckCommand:
		return true
	default:
		return false
	}
}

// AppStatus is the high-level state of an application.
type AppStatus string

const (
	AppStatusCreated   AppStatus = "created"
	AppStatusDeploying AppStatus = "deploying"
	AppStatusRunning   AppStatus = "running"
	AppStatusStopped   AppStatus = "stopped"
	AppStatusFailed    AppStatus = "failed"
)

// Application is a deployable workload owned by a workspace.
type Application struct {
	UIDModel
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index:idx_app_workspace_name,unique;not null"`
	Name        string `json:"name" gorm:"index:idx_app_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI; not unique.
	DisplayName string        `json:"display_name"`
	SourceType  AppSourceType `json:"source_type" gorm:"not null;default:image"`
	// Generated once and reused as the proxy upstream; legacy apps fall back to "mb-app-<id>".
	Alias string `json:"alias,omitempty" gorm:"index"`
	// In cluster mode it is a placement hint; Swarm schedules subject to PlacementConstraints.
	ServerID         uint   `json:"server_id" gorm:"index;not null;default:0"`
	ClusterID        uint   `json:"cluster_id" gorm:"index;not null;default:0"`
	ServerName       string `json:"server_name,omitempty" gorm:"-"`
	GitOpsSourceName string `json:"gitops_source_name,omitempty" gorm:"-"`
	// Transient: populated only on the detail read in cluster mode.
	Nodes []NodePlacement `json:"nodes,omitempty" gorm:"-"`
	// Transient, and nil when the swarm could not be asked, so the UI can tell unknown from zero.
	RunningReplicas *uint64 `json:"running_replicas,omitempty" gorm:"-"`
	DesiredReplicas *uint64 `json:"desired_replicas,omitempty" gorm:"-"`

	// Replicas, PlacementConstraints and UpdateConfig apply only to the service runtime.
	RuntimeKind          RuntimeKind          `json:"runtime_kind" gorm:"not null;default:container"`
	Replicas             int                  `json:"replicas" gorm:"not null;default:1"`
	PlacementConstraints []string             `json:"placement_constraints,omitempty" gorm:"serializer:json"`
	UpdateConfig         *ServiceUpdateConfig `json:"update_config,omitempty" gorm:"serializer:json"`

	// Empty for ad-hoc apps, which fall back to a generated avatar.
	Icon string `json:"icon,omitempty"`

	// Tag defaults to "latest" when composing the pull ref.
	Image string `json:"image"`
	Tag   string `json:"tag,omitempty"`

	GitRepo string `json:"git_repo,omitempty"`
	GitRef  string `json:"git_ref,omitempty"`

	// Git source only; ignored for image-source apps.
	BuildMethod AppBuildMethod    `json:"build_method,omitempty" gorm:"not null;default:auto"`
	Builder     string            `json:"builder,omitempty"`
	Buildpacks  []string          `json:"buildpacks,omitempty" gorm:"serializer:json"`
	BuildEnv    map[string]string `json:"build_env,omitempty" gorm:"serializer:json"`

	// Part of the registry cache ref, so a bump points builds at a new ref rather than deleting anything.
	CacheGeneration uint `json:"cache_generation" gorm:"not null;default:0"`
	// While it trails CacheGeneration the next build is forced cold, for runners whose cache has no ref.
	CacheBuiltGeneration uint `json:"cache_built_generation" gorm:"not null;default:0"`

	// Both nil means an anonymous (public) source.
	RegistryID      *uint `json:"registry_id,omitempty" gorm:"index"`
	GitRepositoryID *uint `json:"git_repository_id,omitempty" gorm:"index"`

	// The stack's DockerName becomes the Docker Compose project label on the app's containers.
	StackID *uint `json:"stack_id,omitempty" gorm:"index"`

	TemplateInstallID *uint `json:"template_install_id,omitempty" gorm:"index"`

	// Set by the platform, never user input: it is the trust boundary for AllowOfficialImageUser.
	OfficialTemplate bool `json:"official_template" gorm:"not null;default:false"`

	// Keys under the reserved "miabi.io/" prefix are platform-managed.
	Metadata Metadata `json:"metadata,omitempty" gorm:"serializer:json"`

	// Never used for selection or grouping, and carries no reserved keys.
	Annotations Metadata `json:"annotations,omitempty" gorm:"serializer:json"`

	// Reserved prefixes (io.miabi.*, miabi.*, com.docker.*) are stripped so they cannot spoof platform labels.
	ContainerLabels map[string]string `json:"container_labels,omitempty" gorm:"serializer:json"`
	// none leaves the app running, for apps that watch their own config file.
	ReloadPolicy string `json:"reload_policy,omitempty"`

	Command     []string   `json:"command,omitempty" gorm:"serializer:json"`
	Mounts      []AppMount `json:"mounts,omitempty" gorm:"serializer:json"` // attached volumes
	Port        int        `json:"port,omitempty"`                          // primary container port
	MemoryBytes int64      `json:"memory_bytes"`                            // 0 = unlimited
	NanoCPUs    int64      `json:"nano_cpus"`                               // 0 = unlimited; 1 core = 1e9
	// Consumes quota only while running; resolved to concrete devices on the node at each deploy.
	GPUCount int `json:"gpu_count" gorm:"not null;default:0"`
	// Empty means any enabled GPU on the app's node.
	GPUKind string `json:"gpu_kind,omitempty"`
	// Under the restricted profile it must be a non-root numeric uid; managed volumes are chowned to it.
	RunAsUser string `json:"run_as_user,omitempty"`
	// Allow-listed in capabilities.go and gated on the workspace; both need a redeploy.
	AddCapabilities []string `json:"add_capabilities,omitempty" gorm:"serializer:json"`
	Devices         []string `json:"devices,omitempty" gorm:"serializer:json"`
	// They can tighten the workspace's security profile but never loosen it.
	ReadOnlyRootFilesystem bool            `json:"read_only_root_filesystem" gorm:"not null;default:false"`
	NoNewPrivileges        bool            `json:"no_new_privileges" gorm:"not null;default:false"`
	DropCapabilities       []string        `json:"drop_capabilities,omitempty" gorm:"serializer:json"`
	RestartPolicy          RestartPolicy   `json:"restart_policy" gorm:"not null;default:unless-stopped"`
	ImagePullPolicy        ImagePullPolicy `json:"image_pull_policy" gorm:"not null;default:always"`

	// With http or command a deploy waits for healthy; HealthcheckPort 0 means the app's primary port.
	HealthcheckType               HealthcheckType `json:"healthcheck_type" gorm:"not null;default:image"`
	HealthcheckHTTPPath           string          `json:"healthcheck_http_path"`
	HealthcheckPort               int             `json:"healthcheck_port"`
	HealthcheckCommand            string          `json:"healthcheck_command"`
	HealthcheckIntervalSeconds    int             `json:"healthcheck_interval_seconds" gorm:"not null;default:30"`
	HealthcheckTimeoutSeconds     int             `json:"healthcheck_timeout_seconds" gorm:"not null;default:5"`
	HealthcheckRetries            int             `json:"healthcheck_retries" gorm:"not null;default:3"`
	HealthcheckStartPeriodSeconds int             `json:"healthcheck_start_period_seconds" gorm:"not null;default:0"`

	// Clears on the first native deploy, which recreates the container under Miabi conventions.
	Imported bool `json:"imported" gorm:"not null;default:false"`

	// Generated once on first expose so the public URL survives renames and redeploys.
	ExternalLabel string `json:"external_label,omitempty" gorm:"index"`

	Status           AppStatus `json:"status" gorm:"not null;default:created"`
	CurrentReleaseID *uint     `json:"current_release_id"`
	// Start/Restart then redeploy instead of reusing the stale container; any successful deploy clears it.
	RedeployRequired bool `json:"redeploy_required" gorm:"not null;default:false"`

	// Deploys are not recorded here, or a restart would look like a release; nil means unknown, not never.
	LastLifecycleAt     *time.Time `json:"last_lifecycle_at,omitempty" gorm:"index"`
	LastLifecycleAction string     `json:"last_lifecycle_action,omitempty"`
	LastLifecycleByID   *uint      `json:"last_lifecycle_by_id,omitempty" gorm:"index"`

	DeployStrategy DeployStrategy `json:"deploy_strategy" gorm:"not null;default:rolling"`

	ReconcilePolicy ReconcilePolicy `json:"reconcile_policy" gorm:"not null;default:inherit"`

	CanaryInitialWeight       int `json:"canary_initial_weight" gorm:"not null;default:10"`
	CanaryStepWeight          int `json:"canary_step_weight" gorm:"not null;default:20"`
	CanaryStepIntervalSeconds int `json:"canary_step_interval_seconds" gorm:"not null;default:60"`

	// Cleared on promote/abort and superseded by any normal deploy.
	CanaryReleaseID *uint `json:"canary_release_id,omitempty"`
	CanaryWeight    int   `json:"canary_weight" gorm:"not null;default:0"`

	CanaryMode CanaryMode `json:"canary_mode" gorm:"not null;default:auto"`

	// CanaryPausedAt holds the ramp where it stands; nil means it is advancing.
	CanaryPausedAt *time.Time `json:"canary_paused_at,omitempty"`

	CanaryExclusive bool              `json:"canary_exclusive" gorm:"not null;default:false"`
	CanaryPriority  int               `json:"canary_priority" gorm:"not null;default:0"`
	CanaryMatch     []CanaryMatchRule `json:"canary_match,omitempty" gorm:"serializer:json"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	EnvVars       []AppEnvVar    `json:"env_vars,omitempty" gorm:"foreignKey:ApplicationID"`
	Networks      []Network      `json:"networks,omitempty" gorm:"many2many:application_networks;"`
	Ports         []AppPort      `json:"ports,omitempty" gorm:"foreignKey:ApplicationID"`
	Stack         *Stack         `json:"stack,omitempty" gorm:"foreignKey:StackID;constraint:OnDelete:SET NULL"`
	Registry      *Registry      `json:"-" gorm:"foreignKey:RegistryID;constraint:OnDelete:SET NULL"`
	GitRepository *GitRepository `json:"-" gorm:"foreignKey:GitRepositoryID;constraint:OnDelete:SET NULL"`
}

// Without an override, a digest pinned via MetaDigest wins over Tag, so the node runs exactly that image.
func (a *Application) ImageRef(tagOverride string) string {
	if d := a.Metadata[MetaDigest]; tagOverride == "" && d != "" && a.Image != "" && !strings.Contains(a.Image, "@") {
		return a.Image + "@" + d
	}
	return ComposeImageRef(a.Image, firstNonEmpty(tagOverride, a.Tag))
}

func ComposeImageRef(image, tag string) string {
	if image == "" {
		return image
	}
	// Already pinned by digest (repo@sha256:...) or tag (after the last "/").
	if strings.Contains(image, "@") {
		return image
	}
	lastSlash := strings.LastIndex(image, "/")
	if strings.Contains(image[lastSlash+1:], ":") {
		return image
	}
	if tag == "" {
		tag = "latest"
	}
	return image + ":" + tag
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// A digest-pinned ref is returned as the image with an empty tag.
func SplitImageRef(ref string) (image, tag string) {
	if ref == "" {
		return "", ""
	}
	if strings.Contains(ref, "@") {
		return ref, "" // digest pin — cannot be represented as a tag
	}
	lastSlash := strings.LastIndex(ref, "/")
	seg := ref[lastSlash+1:]
	if i := strings.LastIndex(seg, ":"); i >= 0 {
		return ref[:lastSlash+1] + seg[:i], seg[i+1:]
	}
	return ref, ""
}

const (
	ReloadRestart = "restart"
	ReloadNone    = "none"
)

type AppMount struct {
	VolumeID   uint   `json:"volume_id"`
	DockerName string `json:"docker_name"`
	Path       string `json:"path"`
	HostPreset string `json:"host_preset,omitempty"`

	HostPath string `json:"host_path,omitempty"`

	ConfigID  uint   `json:"config_id,omitempty"`
	ConfigKey string `json:"config_key,omitempty"`
	Mode      string `json:"mode,omitempty"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

type AppEnvVar struct {
	ID            uint      `json:"id" gorm:"primaryKey"`
	ApplicationID uint      `json:"application_id" gorm:"index:idx_env_app_key,unique;not null"`
	Key           string    `json:"key" gorm:"index:idx_env_app_key,unique;not null"`
	Value         string    `json:"value"` // plaintext for non-secret; ciphertext when IsSecret
	IsSecret      bool      `json:"is_secret" gorm:"not null;default:false"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DeploymentStatus is the state of a single deploy attempt.
type DeploymentStatus string

const (
	DeploymentPending   DeploymentStatus = "pending"
	DeploymentBuilding  DeploymentStatus = "building"
	DeploymentDeploying DeploymentStatus = "deploying"
	DeploymentCanary    DeploymentStatus = "canary"
	DeploymentSucceeded DeploymentStatus = "succeeded"
	DeploymentFailed    DeploymentStatus = "failed"
)

// IsTerminal reports whether the deployment has reached a final state.
func (s DeploymentStatus) IsTerminal() bool {
	return s == DeploymentSucceeded || s == DeploymentFailed
}

// Deployment is a single attempt to deploy an application.
type Deployment struct {
	ID            uint             `json:"id" gorm:"primaryKey"`
	Number        int              `json:"number" gorm:"index:idx_deploy_app_number,unique;not null;default:0"`
	ApplicationID uint             `json:"application_id" gorm:"index:idx_deploy_app_number,unique;index;not null"`
	Status        DeploymentStatus `json:"status" gorm:"not null;default:pending"`
	Image         string           `json:"image"`
	Trigger       string           `json:"trigger"` // manual | rollback | auto | pipeline | reconcile
	TriggeredByID *uint            `json:"triggered_by_id,omitempty" gorm:"index"`
	Strategy      DeployStrategy   `json:"strategy" gorm:"not null;default:rolling"` // rollout method for this deploy
	Commit        string           `json:"commit,omitempty"`
	ImageID       *uint            `json:"image_id,omitempty"`
	ImageTag      string           `json:"image_tag,omitempty"`                    // policy version tag of a pipeline build, e.g. v1.4.2
	PipelineRunID *uint            `json:"pipeline_run_id,omitempty" gorm:"index"` // run whose deploy step created it
	RegistryID    *uint            `json:"registry_id,omitempty"`
	NoCache       bool             `json:"no_cache,omitempty" gorm:"not null;default:false"`
	RunnerID      *uint            `json:"runner_id,omitempty"`
	ContainerID   string           `json:"container_id,omitempty"`
	Logs          string           `json:"logs,omitempty" gorm:"type:text"`
	LogRef        string           `json:"log_ref,omitempty"`
	LogBytes      int64            `json:"log_bytes,omitempty"`
	LogLines      int              `json:"log_lines,omitempty"`
	LogTruncated  bool             `json:"log_truncated,omitempty"`
	Error         string           `json:"error,omitempty" gorm:"type:text"`
	StartedAt     *time.Time       `json:"started_at"`
	FinishedAt    *time.Time       `json:"finished_at"`
	CreatedAt     time.Time        `json:"created_at"`
	Current       bool             `json:"current" gorm:"-"`
}

type DeploymentWithActor struct {
	Deployment
	TriggeredByName string `json:"triggered_by_name,omitempty"`
}

func (d *Deployment) BeforeCreate(tx *gorm.DB) error {
	if d.Number != 0 {
		return nil
	}
	var max int
	if err := tx.Model(&Deployment{}).
		Where("application_id = ?", d.ApplicationID).
		Select("COALESCE(MAX(number), 0)").Scan(&max).Error; err != nil {
		return err
	}
	d.Number = max + 1
	return nil
}

// Release is a successfully deployed, rollback-able version of an application.
type Release struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	ApplicationID uint   `json:"application_id" gorm:"index:idx_release_app_version,unique;index;not null"`
	DeploymentID  uint   `json:"deployment_id" gorm:"not null"`
	Version       int    `json:"version" gorm:"index:idx_release_app_version,unique;not null"`
	Image         string `json:"image" gorm:"not null"`
	ContainerID   string `json:"container_id"`
	// Tells whether ContainerID is a container or a service id when the release is retired.
	RuntimeKind   RuntimeKind `json:"runtime_kind" gorm:"size:16"`
	Active        bool        `json:"active" gorm:"not null;default:false"`
	Adopted       bool        `json:"adopted" gorm:"not null;default:false"`
	Pinned        bool        `json:"pinned" gorm:"not null;default:false"`
	Digest        string      `json:"digest,omitempty"`                       // sha256:… the release ran
	Commit        string      `json:"commit,omitempty"`                       // source commit, when known
	PipelineRunID *uint       `json:"pipeline_run_id,omitempty" gorm:"index"` // producing run
	ImageID       *uint       `json:"image_id,omitempty" gorm:"index"`        // catalog artifact
	ImageTag      string      `json:"image_tag,omitempty"`                    // policy version tag, e.g. v1.4.2
	EnvironmentID *uint       `json:"environment_id,omitempty" gorm:"index"`  // promotion stage

	CreatedAt time.Time `json:"created_at"`
}
