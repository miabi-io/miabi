// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// AppBuildMethod selects how a git-source application's image is built. It only
// applies to git apps (image apps pull a prebuilt image).
type AppBuildMethod string

const (
	// BuildAuto inspects the cloned repository at build time: a Dockerfile in the
	// tree builds with Docker, otherwise Cloud Native Buildpacks build it. Default.
	BuildAuto AppBuildMethod = "auto"
	// BuildDockerfile always builds the repo's Dockerfile.
	BuildDockerfile AppBuildMethod = "dockerfile"
	// BuildBuildpack always builds with Cloud Native Buildpacks (no Dockerfile
	// required); the resulting image runs via the CNB launcher.
	BuildBuildpack AppBuildMethod = "buildpack"
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
	// DeployRecreate stops the current container before starting the new one
	// (brief downtime; use when two versions cannot run at once).
	DeployRecreate DeployStrategy = "recreate"
	// DeployRolling starts the new container, waits for health, then retires the
	// old one (zero-downtime for a single replica). Default.
	DeployRolling DeployStrategy = "rolling"
	// DeployCanary runs the new release alongside the stable one and shifts
	// weighted traffic to it progressively (platform-driven).
	DeployCanary DeployStrategy = "canary"
)

// ValidDeployStrategy reports whether s is a known strategy.
func ValidDeployStrategy(s DeployStrategy) bool {
	switch s {
	case DeployRecreate, DeployRolling, DeployCanary:
		return true
	default:
		return false
	}
}

// RuntimeKind is how an application runs: a single Docker container (default,
// works on plain Docker) or a replicated Swarm service (cluster mode). It is
// auto-gated on the manager being a swarm manager — a "service" app only deploys
// when cluster mode is enabled.
type RuntimeKind string

const (
	// RuntimeContainer runs the app as one plain Docker container on its node.
	RuntimeContainer RuntimeKind = "container"
	// RuntimeService runs the app as a replicated Swarm service on the workspace
	// overlay network.
	RuntimeService RuntimeKind = "service"
)

// ValidRuntimeKind reports whether k is a known runtime kind.
func ValidRuntimeKind(k RuntimeKind) bool {
	switch k {
	case RuntimeContainer, RuntimeService:
		return true
	default:
		return false
	}
}

// NodePlacement is where one slice of a cluster app's replicas runs: a node's
// display name and how many of the app's running tasks the scheduler placed on
// it. Transient — built on read from live Swarm task placement.
type NodePlacement struct {
	// Name is the node's Miabi display name, or a short swarm node id when the node
	// is not (yet) correlated to a Miabi server record.
	Name string `json:"name"`
	// Tasks is the number of the app's running tasks on this node.
	Tasks int `json:"tasks"`
}

// ServiceUpdateConfig tunes how Swarm rolls out a service update for cluster
// apps (parallelism = tasks updated at once; delay = pause between batches).
type ServiceUpdateConfig struct {
	Parallelism  int `json:"parallelism,omitempty"`
	DelaySeconds int `json:"delay_seconds,omitempty"`
}

// RestartPolicy is the Docker restart policy applied to an app's container. It
// mirrors the engine's policies; the platform default is "unless-stopped".
type RestartPolicy string

const (
	RestartNo            RestartPolicy = "no"             // never restart
	RestartAlways        RestartPolicy = "always"         // always restart
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

// ImagePullPolicy decides whether a deploy pulls the app's image from the
// registry. The platform default is "always" (a redeploy fetches the current
// tag); "if-not-present" reuses a locally cached image; "never" requires it to be
// present already (air-gapped / locally built). Digest-pinned refs are immutable,
// so they are never re-pulled when already present regardless of policy.
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
	HealthcheckNone    HealthcheckType = "none"    // no healthcheck
	HealthcheckHTTP    HealthcheckType = "http"    // HTTP GET against a path/port
	HealthcheckCommand HealthcheckType = "command" // shell command (CMD-SHELL)
)

// ValidHealthcheckType reports whether t is a known healthcheck type.
func ValidHealthcheckType(t HealthcheckType) bool {
	switch t {
	case HealthcheckNone, HealthcheckHTTP, HealthcheckCommand:
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
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_app_workspace_name,unique;not null"`
	// Name is the unique, URL/CLI/docker handle (lowercase [a-z0-9-]) scoped to
	// the workspace. Renamed from the former "slug"; the numeric ID/UID remain the
	// stable internal references.
	Name string `json:"name" gorm:"index:idx_app_workspace_name,unique;not null"`
	// DisplayName is the free-text label shown in the UI. Renamed from the former
	// "name"; not unique.
	DisplayName string        `json:"display_name"`
	SourceType  AppSourceType `json:"source_type" gorm:"not null;default:image"`
	// Alias is the stable in-network DNS name + container hostname for the app,
	// shaped "mb-app-<token>-<id>". Generated once at creation and reused as the
	// reverse-proxy upstream so it survives redeploys. Empty on legacy apps,
	// which fall back to "mb-app-<id>".
	Alias string `json:"alias,omitempty" gorm:"index"`
	// ServerID is the node this app runs on (0 = local control-plane node). In
	// cluster mode (RuntimeService) it is a placement hint rather than a fixed
	// node — Swarm schedules the tasks subject to PlacementConstraints.
	ServerID uint `json:"server_id" gorm:"index;not null;default:0"`
	// ServerName is the display name of the node (transient; populated on read so
	// the UI can show where the app runs). Empty if the node is unknown.
	ServerName string `json:"server_name,omitempty" gorm:"-"`
	// Nodes is the real per-node replica placement of a cluster (RuntimeService)
	// app: where the Swarm scheduler actually placed its running tasks (transient;
	// populated only on the app-detail read, and only in cluster mode). Empty for
	// container apps, whose single node is ServerName.
	Nodes []NodePlacement `json:"nodes,omitempty" gorm:"-"`

	// Cluster runtime. RuntimeKind defaults to "container" (single container on
	// plain Docker); "service" runs the app as a replicated Swarm service on the
	// workspace overlay (only when cluster mode is enabled). Replicas,
	// PlacementConstraints and UpdateConfig apply to the service runtime.
	RuntimeKind          RuntimeKind          `json:"runtime_kind" gorm:"not null;default:container"`
	Replicas             int                  `json:"replicas" gorm:"not null;default:1"`
	PlacementConstraints []string             `json:"placement_constraints,omitempty" gorm:"serializer:json"`
	UpdateConfig         *ServiceUpdateConfig `json:"update_config,omitempty" gorm:"serializer:json"`

	// Icon is an optional logo for the app (a URL or an "mdi-…" class) shown in
	// the UI. Set from a marketplace template on install; empty for ad-hoc apps,
	// which fall back to a generated avatar.
	Icon string `json:"icon,omitempty"`

	// Image source. Image is the repository (e.g. "nginx", "ghcr.io/org/app");
	// Tag is optional and defaults to "latest" when composing the pull ref.
	Image string `json:"image"`
	Tag   string `json:"tag,omitempty"`

	// Git source.
	GitRepo string `json:"git_repo,omitempty"`
	GitRef  string `json:"git_ref,omitempty"`

	// Build config (git source only). BuildMethod selects how the image is built:
	// auto (Dockerfile if present, else buildpacks), dockerfile, or buildpack.
	// Builder optionally overrides the buildpack builder image (empty = platform
	// default); Buildpacks pins extra buildpacks to apply; BuildEnv supplies build
	// arguments to the buildpack build. All are ignored for image-source apps.
	BuildMethod AppBuildMethod    `json:"build_method,omitempty" gorm:"not null;default:auto"`
	Builder     string            `json:"builder,omitempty"`
	Buildpacks  []string          `json:"buildpacks,omitempty" gorm:"serializer:json"`
	BuildEnv    map[string]string `json:"build_env,omitempty" gorm:"serializer:json"`

	// Credentials. RegistryID selects a stored registry credential used to pull
	// private images; GitRepositoryID selects a stored Git credential used to
	// clone private repos. Both nil = anonymous (public) source.
	RegistryID      *uint `json:"registry_id,omitempty" gorm:"index"`
	GitRepositoryID *uint `json:"git_repository_id,omitempty" gorm:"index"`

	// StackID optionally groups the app under a Stack (nil = ungrouped). When
	// set, the stack's DockerName is applied to the app's containers as the
	// Docker Compose project label.
	StackID *uint `json:"stack_id,omitempty" gorm:"index"`

	// TemplateInstallID links the app back to the marketplace template install it
	// was created from (nil = not installed from a template). Lets the UI show
	// provenance ("installed from <template>") and jump to the install.
	TemplateInstallID *uint `json:"template_install_id,omitempty" gorm:"index"`

	// OfficialTemplate marks an app created from an *official* marketplace template.
	// It is set by the platform at install time (never by user input) and is the
	// trust boundary for the AllowOfficialImageUser plan capability, which lets such
	// apps keep the image's default user under the restricted security profile.
	OfficialTemplate bool `json:"official_template" gorm:"not null;default:false"`

	// Metadata holds free-form labels (provenance, grouping, declarative/GitOps).
	// Keys under the reserved "miabi.io/" prefix are platform-managed.
	Metadata Metadata `json:"metadata,omitempty" gorm:"serializer:json"`

	// Annotations holds free-form, non-identifying descriptive metadata (the
	// manifest's metadata.annotations). Unlike Metadata/labels it is never used
	// for selection or grouping and carries no reserved keys — purely human/tool
	// notes (owner, description, links). Persisted as JSON.
	Annotations Metadata `json:"annotations,omitempty" gorm:"serializer:json"`

	// ContainerLabels are user-defined Docker labels stamped onto this app's
	// container(s)/service at deploy time, for label-driven ecosystem tools
	// (Traefik, autoheal, Watchtower, …). Distinct from Metadata (a DB-record
	// concern): these land on the real Docker object. Keys under Miabi's reserved
	// Docker prefixes (io.miabi.*, miabi.*, com.docker.*) are stripped/rejected so
	// user labels can never override or spoof platform system labels. Persisted as
	// JSON. Editing them requires a redeploy to take effect (like ports/volumes).
	ContainerLabels map[string]string `json:"container_labels,omitempty" gorm:"serializer:json"`

	// Runtime config.
	Command     []string   `json:"command,omitempty" gorm:"serializer:json"`
	Mounts      []AppMount `json:"mounts,omitempty" gorm:"serializer:json"` // attached volumes
	Port        int        `json:"port,omitempty"`                          // primary container port
	MemoryBytes int64      `json:"memory_bytes"`                            // 0 = unlimited
	NanoCPUs    int64      `json:"nano_cpus"`                               // 0 = unlimited; 1 core = 1e9
	// GPUCount is the number of whole GPU units this app requests (0 = none). It
	// is declarative on the app but only consumes quota while the app is running,
	// and is resolved to concrete devices on the app's node at each deploy.
	GPUCount int `json:"gpu_count" gorm:"not null;default:0"`
	// GPUKind narrows the request to a vendor or model when set ("nvidia",
	// "NVIDIA A100-…"); empty = any enabled GPU on the app's node.
	GPUKind string `json:"gpu_kind,omitempty"`
	// RestartPolicy is the Docker restart policy for the app's container.
	// Defaults to unless-stopped (the platform's historical behavior).
	RestartPolicy RestartPolicy `json:"restart_policy" gorm:"not null;default:unless-stopped"`
	// ImagePullPolicy decides whether a deploy pulls the image. Defaults to
	// always (the platform's historical behavior of fetching the tag each deploy).
	ImagePullPolicy ImagePullPolicy `json:"image_pull_policy" gorm:"not null;default:always"`

	// Healthcheck. Type none disables it; http builds a curl/wget probe against
	// HealthcheckHTTPPath on HealthcheckPort (0 = the app's primary port);
	// command runs HealthcheckCommand via the shell. When a healthcheck is set,
	// a deploy waits for the container to report healthy before going live.
	HealthcheckType               HealthcheckType `json:"healthcheck_type" gorm:"not null;default:none"`
	HealthcheckHTTPPath           string          `json:"healthcheck_http_path"`
	HealthcheckPort               int             `json:"healthcheck_port"`
	HealthcheckCommand            string          `json:"healthcheck_command"`
	HealthcheckIntervalSeconds    int             `json:"healthcheck_interval_seconds" gorm:"not null;default:30"`
	HealthcheckTimeoutSeconds     int             `json:"healthcheck_timeout_seconds" gorm:"not null;default:5"`
	HealthcheckRetries            int             `json:"healthcheck_retries" gorm:"not null;default:3"`
	HealthcheckStartPeriodSeconds int             `json:"healthcheck_start_period_seconds" gorm:"not null;default:0"`

	// Imported marks an app adopted from a pre-existing (hand-run / compose)
	// container rather than created through Miabi. It clears on the first
	// native deploy, which recreates the container under Miabi conventions.
	Imported bool `json:"imported" gorm:"not null;default:false"`

	// ExternalLabel is the stable DNS label used for one-click external access
	// (`<label>.<base-domain>`). Generated once on first expose (default
	// `<slug>-<token>`) so the public URL survives renames/redeploys.
	ExternalLabel string `json:"external_label,omitempty" gorm:"index"`

	Status           AppStatus `json:"status" gorm:"not null;default:created"`
	CurrentReleaseID *uint     `json:"current_release_id"`
	// RedeployRequired is set when config changes while the app is stopped (the
	// running container, if any, is stale). Start/Restart then redeploy instead
	// of reusing the old container, and any successful deploy clears it.
	RedeployRequired bool `json:"redeploy_required" gorm:"not null;default:false"`

	// DeployStrategy is the app's default rollout method, applied when a deploy
	// does not specify one. See DeployStrategy constants.
	DeployStrategy DeployStrategy `json:"deploy_strategy" gorm:"not null;default:rolling"`

	// Canary tuning (used when DeployStrategy is canary or a deploy overrides to
	// canary). The platform starts at CanaryInitialWeight and adds
	// CanaryStepWeight every CanaryStepIntervalSeconds until it reaches 100%.
	CanaryInitialWeight       int `json:"canary_initial_weight" gorm:"not null;default:10"`
	CanaryStepWeight          int `json:"canary_step_weight" gorm:"not null;default:20"`
	CanaryStepIntervalSeconds int `json:"canary_step_interval_seconds" gorm:"not null;default:60"`

	// Canary live state. When CanaryReleaseID is set, a second (canary) container
	// runs alongside the stable release and the reverse-proxy splits traffic:
	// CanaryWeight percent goes to the canary, the rest to the stable release.
	// Cleared on promote/abort and superseded by any normal deploy.
	CanaryReleaseID *uint `json:"canary_release_id,omitempty"`
	CanaryWeight    int   `json:"canary_weight" gorm:"not null;default:0"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	EnvVars  []AppEnvVar `json:"env_vars,omitempty" gorm:"foreignKey:ApplicationID"`
	Networks []Network   `json:"networks,omitempty" gorm:"many2many:application_networks;"`
	Ports    []AppPort   `json:"ports,omitempty" gorm:"foreignKey:ApplicationID"`
	Stack    *Stack      `json:"stack,omitempty" gorm:"foreignKey:StackID;constraint:OnDelete:SET NULL"`
	// Credential FKs declared so deleting a referenced registry/git credential
	// nulls the pointer here (ON DELETE SET NULL) instead of dangling. The app
	// then pulls/clones anonymously until re-linked. Associations not serialized.
	Registry      *Registry      `json:"-" gorm:"foreignKey:RegistryID;constraint:OnDelete:SET NULL"`
	GitRepository *GitRepository `json:"-" gorm:"foreignKey:GitRepositoryID;constraint:OnDelete:SET NULL"`
}

// ImageRef composes the effective pull reference from Image and Tag.
// When tagOverride is non-empty it takes precedence over the app's Tag. If the
// image already carries a tag or digest, it is used verbatim; otherwise the tag
// (or "latest") is appended.
func (a *Application) ImageRef(tagOverride string) string {
	return ComposeImageRef(a.Image, firstNonEmpty(tagOverride, a.Tag))
}

// ComposeImageRef appends tag (defaulting to "latest") to a bare image, leaving
// refs that already specify a tag or digest untouched.
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

// SplitImageRef is the inverse of ComposeImageRef: it splits a pull reference
// into its repository and tag (e.g. "ghcr.io/org/app:v2" -> "ghcr.io/org/app",
// "v2"). A digest-pinned ref is returned as the image with an empty tag.
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

// AppMount attaches storage to an application at a container path. It is either
// a managed workspace volume (VolumeID/DockerName) or — for privileged
// workspaces only — an allow-listed host bind identified by HostPreset (see
// package hostmount). The two are mutually exclusive; the host source path is
// resolved server-side from the preset, never stored from client input.
type AppMount struct {
	VolumeID   uint   `json:"volume_id"`
	DockerName string `json:"docker_name"`
	Path       string `json:"path"`
	HostPreset string `json:"host_preset,omitempty"` // set => privileged host bind, not a volume
	// HostPath is the bind source for a "host" driver volume (denormalized from the
	// volume at attach time so the runtime binds it without a volume lookup). When
	// set, the mount is a bind of HostPath, not a Docker named volume.
	HostPath string `json:"host_path,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// AppEnvVar is an environment variable for an application. Secret values are
// encrypted at rest.
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
	// DeploymentCanary is a non-terminal state: the canary container is live and
	// serving a weighted share of traffic while the rollout progresses. It becomes
	// terminal when the canary is promoted/superseded (succeeded) or aborted
	// (failed), so the deployment's log stream stays open for the whole rollout.
	DeploymentCanary DeploymentStatus = "canary"
	// DeploymentSucceeded is the terminal success state of a deploy attempt.
	// Whether the app is currently live is a property of the active Release, not
	// of a historical deployment.
	DeploymentSucceeded DeploymentStatus = "succeeded"
	DeploymentFailed    DeploymentStatus = "failed"
)

// IsTerminal reports whether the deployment has reached a final state.
func (s DeploymentStatus) IsTerminal() bool {
	return s == DeploymentSucceeded || s == DeploymentFailed
}

// Deployment is a single attempt to deploy an application.
type Deployment struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// Number is the per-application sequential deployment number (1, 2, 3…),
	// independent of the global ID. It mirrors Release.Version: stable and
	// user-friendly for managing an app's deploy history (the first deploy is #1,
	// the second #2, …). Assigned automatically in BeforeCreate; the composite
	// unique index keeps it gap-free per app and prevents duplicates.
	Number        int              `json:"number" gorm:"index:idx_deploy_app_number,unique;not null;default:0"`
	ApplicationID uint             `json:"application_id" gorm:"index:idx_deploy_app_number,unique;index;not null"`
	Status        DeploymentStatus `json:"status" gorm:"not null;default:pending"`
	Image         string           `json:"image"`
	Trigger       string           `json:"trigger"`                                  // manual | rollback | auto | pipeline
	Strategy      DeployStrategy   `json:"strategy" gorm:"not null;default:rolling"` // rollout method for this deploy
	// Commit pins the source revision this deployment was built from. Set by the
	// pipeline runner so a deploy reproduces the exact commit the run captured,
	// even if the app's branch advanced after the run started.
	Commit string `json:"commit,omitempty"`
	// ImageID links a deployment to a prebuilt Image (built by a pipeline run on
	// this node). When set and the image is present locally the deploy worker
	// runs it directly — no rebuild, no pull.
	ImageID *uint `json:"image_id,omitempty"`
	// RegistryID is the registry credential used for this deploy (snapshot of
	// the app's selection, allowing per-deploy override). nil = anonymous.
	RegistryID *uint `json:"registry_id,omitempty"`
	// RunnerID records which runner built the image this deployment rolls out
	// (provenance / "built on runner X"); nil for images built by the internal
	// runner or pulled directly.
	RunnerID    *uint  `json:"runner_id,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
	// Logs is a bounded tail of the build/deploy output for instant display; the
	// full log lives in the log store at LogRef once the deployment is terminal
	// (see plans/log-storage.md). LogRef is empty when the store is disabled or
	// the row predates externalization — readers fall back to this tail.
	Logs         string     `json:"logs,omitempty" gorm:"type:text"`
	LogRef       string     `json:"log_ref,omitempty"`
	LogBytes     int64      `json:"log_bytes,omitempty"`
	LogLines     int        `json:"log_lines,omitempty"`
	LogTruncated bool       `json:"log_truncated,omitempty"`
	Error        string     `json:"error,omitempty" gorm:"type:text"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	CreatedAt    time.Time  `json:"created_at"`

	// Current is a transient flag (not persisted) marking the deployment whose
	// release is the application's active one — i.e. what is live right now.
	Current bool `json:"current" gorm:"-"`
}

// BeforeCreate assigns the per-application sequential Number (MAX+1 scoped to the
// app), mirroring how Release.Version is allocated. Running inside the insert's
// transaction keeps every creation path (deploy, redeploy, pipeline, import)
// consistent without each call site computing the number itself. An explicit
// Number (e.g. a backfill) is left untouched.
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
	ID uint `json:"id" gorm:"primaryKey"`
	// (ApplicationID, Version) is unique: concurrent deploys that both compute the
	// same MAX(version)+1 can no longer both persist — one hits the constraint and
	// rolls back instead of corrupting history with duplicate versions.
	ApplicationID uint   `json:"application_id" gorm:"index:idx_release_app_version,unique;index;not null"`
	DeploymentID  uint   `json:"deployment_id" gorm:"not null"`
	Version       int    `json:"version" gorm:"index:idx_release_app_version,unique;not null"`
	Image         string `json:"image" gorm:"not null"`
	ContainerID   string `json:"container_id"`
	Active        bool   `json:"active" gorm:"not null;default:false"`
	// Adopted marks a release whose ContainerID points at a pre-existing container
	// adopted by the import flow (not created by the deploy pipeline). It lets the
	// Docker browser badge the container "adopted" and is cleared when a native
	// deploy supersedes it.
	Adopted bool `json:"adopted" gorm:"not null;default:false"`
	// Pinned releases are protected from deletion during cleanup.
	Pinned bool `json:"pinned" gorm:"not null;default:false"`

	// Provenance (GitOps & CI/CD). A release generalizes into a promotable
	// artifact: image digest + config snapshot + version + provenance, flowing
	// through environments. These fields are populated when a deploy originates
	// from a pipeline run or a digest-pinned apply; they stay zero for legacy
	// hand-triggered deploys.
	Digest        string `json:"digest,omitempty"`                       // sha256:… the release ran
	Commit        string `json:"commit,omitempty"`                       // source commit, when known
	PipelineRunID *uint  `json:"pipeline_run_id,omitempty" gorm:"index"` // producing run
	ImageID       *uint  `json:"image_id,omitempty" gorm:"index"`        // catalog artifact
	EnvironmentID *uint  `json:"environment_id,omitempty" gorm:"index"`  // promotion stage

	CreatedAt time.Time `json:"created_at"`
}
