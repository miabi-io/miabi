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

// Package declarative is the miabi.io/v1 resource model shared by apply, GitOps, Terraform and marketplace templates.
package declarative

// APIVersion is shared with the marketplace manifest engine so the two stay in lock-step.
const APIVersion = "miabi.io/v1"

type Kind string

const (
	KindApplication Kind = "Application"
	KindStack       Kind = "Stack"
	KindDatabase    Kind = "Database"
	KindVolume      Kind = "Volume"
	// KindRoute exposes an app on a hostname; KindDomain is the owned zone with ownership and default TLS.
	KindRoute  Kind = "Route"
	KindSecret Kind = "Secret"
	// KindSealedSecret is sealed to the workspace public key so it is safe to commit; same vault namespace as Secret.
	KindSealedSecret Kind = "SealedSecret"
	// KindDomain ownership verification is a runtime action, not a declarable field.
	KindDomain Kind = "Domain"
	// KindRegistry is not called a secret because Secret is the vault; apps reference it via spec.registry.
	KindRegistry Kind = "Registry"
	// KindProject bundles a set of resources living in one repo/namespace.
	KindProject Kind = "Project"
	// KindConfig carries named files projected read-only into containers; encrypted at rest like Secret.
	KindConfig Kind = "Config"
	// KindMiddleware is its own resource so one policy can be shared by many routes instead of copied.
	KindMiddleware Kind = "Middleware"
	// KindCronJob runs in the Application's runtime context (same image, env, networks and volumes).
	KindCronJob Kind = "CronJob"
	// KindJob runs only when its runPolicy says so, never again for an unchanged spec.
	KindJob Kind = "Job"
	// KindGitSource declared from a GitOps repository nests one project under another ("app of apps").
	KindGitSource Kind = "GitSource"
	// KindGitRepository never carries the token or SSH key; it is read from the vault.
	KindGitRepository Kind = "GitRepository"
)

var knownKinds = map[Kind]bool{
	KindApplication:   true,
	KindStack:         true,
	KindDatabase:      true,
	KindVolume:        true,
	KindRoute:         true,
	KindSecret:        true,
	KindSealedSecret:  true,
	KindDomain:        true,
	KindRegistry:      true,
	KindProject:       true,
	KindConfig:        true,
	KindMiddleware:    true,
	KindCronJob:       true,
	KindJob:           true,
	KindGitSource:     true,
	KindGitRepository: true,
}

// Custom metadata belongs in Labels or Annotations, never as ad-hoc top-level keys.
type Meta struct {
	Name string `yaml:"name" json:"name"`
	// UID survives restores and renames; when set it is matched ahead of name.
	UID         string            `yaml:"uid,omitempty" json:"uid,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

// Exactly one typed spec pointer is set, determined by Kind.
type Resource struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	Kind       Kind   `yaml:"kind" json:"kind"`
	Metadata   Meta   `yaml:"metadata" json:"metadata"`

	Application   *ApplicationSpec   `yaml:"-" json:"application,omitempty"`
	Stack         *StackSpec         `yaml:"-" json:"stack,omitempty"`
	Database      *DatabaseSpec      `yaml:"-" json:"database,omitempty"`
	Volume        *VolumeSpec        `yaml:"-" json:"volume,omitempty"`
	Route         *RouteSpec         `yaml:"-" json:"route,omitempty"`
	Secret        *SecretSpec        `yaml:"-" json:"secret,omitempty"`
	SealedSecret  *SealedSecretSpec  `yaml:"-" json:"sealedSecret,omitempty"`
	Domain        *DomainSpec        `yaml:"-" json:"domain,omitempty"`
	Registry      *RegistrySpec      `yaml:"-" json:"registry,omitempty"`
	Project       *ProjectSpec       `yaml:"-" json:"project,omitempty"`
	Config        *ConfigSpec        `yaml:"-" json:"config,omitempty"`
	Middleware    *MiddlewareSpec    `yaml:"-" json:"middleware,omitempty"`
	CronJob       *CronJobSpec       `yaml:"-" json:"cronJob,omitempty"`
	Job           *JobSpec           `yaml:"-" json:"job,omitempty"`
	GitSource     *GitSourceSpec     `yaml:"-" json:"gitSource,omitempty"`
	GitRepository *GitRepositorySpec `yaml:"-" json:"gitRepository,omitempty"`
}

func (r Resource) Key() string { return string(r.Kind) + "/" + r.Metadata.Name }

// ApplicationSpec Digest is written by CI; GitOps converges runtime to it.
type ApplicationSpec struct {
	// Source and image are mutually exclusive: an app either builds or pulls.
	Source    *SourceSpec               `yaml:"source,omitempty" json:"source,omitempty"`
	Image     string                    `yaml:"image,omitempty" json:"image,omitempty"`
	Tag       string                    `yaml:"tag,omitempty" json:"tag,omitempty"`
	Digest    string                    `yaml:"digest,omitempty" json:"digest,omitempty"` // sha256:… (immutable pin)
	Command   []string                  `yaml:"command,omitempty" json:"command,omitempty"`
	Ports     []PortSpec                `yaml:"ports,omitempty" json:"ports,omitempty"`
	Env       map[string]string         `yaml:"env,omitempty" json:"env,omitempty"`
	SecretEnv []string                  `yaml:"secretEnv,omitempty" json:"secretEnv,omitempty"` // keys stored encrypted; vault references need none, apply-resolved credentials are added
	Mounts    []MountSpec               `yaml:"mounts,omitempty" json:"mounts,omitempty"`
	Resources *ResourceSpec             `yaml:"resources,omitempty" json:"resources,omitempty"`
	Placement *ApplicationPlacementSpec `yaml:"placement,omitempty" json:"placement,omitempty"`
	// Deployment is how the app runs and how a new release replaces the running one.
	Deployment *DeploymentSpec `yaml:"deployment,omitempty" json:"deployment,omitempty"`
	// Security is checked on apply the way the console checks it.
	Security *SecuritySpec `yaml:"security,omitempty" json:"security,omitempty"`
	// Healthcheck gates releases so a broken one never replaces a working one; omitted keeps the current check.
	Healthcheck *HealthcheckSpec `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	// ContainerLabels reserved keys (io.miabi.*, com.docker.*) are stripped on apply rather than rejected.
	ContainerLabels map[string]string `yaml:"containerLabels,omitempty" json:"containerLabels,omitempty"`
	// Stack optionally names the owning Stack resource.
	Stack string `yaml:"stack,omitempty" json:"stack,omitempty"`
	// Registry empty means an anonymous pull; an undeclared name resolves against existing workspace credentials.
	Registry string `yaml:"registry,omitempty" json:"registry,omitempty"`
	// ExternalLabel is platform-wide unique; a claimed one is ignored for a generated label.
	ExternalLabel string `yaml:"externalLabel,omitempty" json:"externalLabel,omitempty"`
	// ReloadPolicy "restart" (default) redeploys on config change; "none" suits apps that watch their own config.
	ReloadPolicy string `yaml:"reloadPolicy,omitempty" json:"reloadPolicy,omitempty"`
	// Deprecated: use security.runAsUser. Still accepted; setting both is an error.
	DeprecatedRunAsUser string `yaml:"runAsUser,omitempty" json:"runAsUser,omitempty"`
	// Deprecated: use deployment.strategy. Still accepted; setting both is an error.
	DeprecatedStrategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	// ConfigFP is derived state filled on both sides of the diff, so a config content change converges.
	ConfigFP string `yaml:"-" json:"-"`
}

// ApplicationPlacementSpec is where an application runs.
type ApplicationPlacementSpec struct {
	// Location defaults to the stack, a mounted volume or referenced databases, else the workspace default; fixed once created.
	Location string `yaml:"location,omitempty" json:"location,omitempty"`
	// Constraints use Swarm syntax (node.labels.disk==ssd); an empty list clears them, omitting keeps them.
	Constraints []string `yaml:"constraints,omitempty" json:"constraints,omitempty"`
}

// DeploymentSpec is how an application runs and rolls out.
type DeploymentSpec struct {
	// Runtime service needs a location that runs a swarm; omitted, an existing app keeps its runtime.
	Runtime string `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	// Replicas is 1 to 100; omitted keeps the current count, so a console scale is not undone.
	Replicas int `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	// Strategy is recreate, rolling or canary; omitted keeps the configured strategy.
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	// Update tunes how Swarm rolls a new release of a service out.
	Update *UpdateSpec `yaml:"update,omitempty" json:"update,omitempty"`
}

// UpdateSpec is a service rollout: how many tasks are replaced at once, and the pause between batches.
type UpdateSpec struct {
	Parallelism  int `yaml:"parallelism,omitempty" json:"parallelism,omitempty"`
	DelaySeconds int `yaml:"delaySeconds,omitempty" json:"delaySeconds,omitempty"`
}

// HealthcheckSpec applies only stated fields; the rest keep their current value.
type HealthcheckSpec struct {
	// Type is http, command, image (the image's HEALTHCHECK, default) or none (not even the image's).
	Type string `yaml:"type" json:"type"`
	// Path is the URL path an http check requests, e.g. /healthz.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// Port is the container port an http check probes. Omitted, the app's first port.
	Port int `yaml:"port,omitempty" json:"port,omitempty"`
	// Command needs a shell in the image; an http check does not.
	Command            string `yaml:"command,omitempty" json:"command,omitempty"`
	IntervalSeconds    int    `yaml:"intervalSeconds,omitempty" json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int    `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	Retries            int    `yaml:"retries,omitempty" json:"retries,omitempty"`
	StartPeriodSeconds int    `yaml:"startPeriodSeconds,omitempty" json:"startPeriodSeconds,omitempty"`
}

// PlacementSpec is where a stack or volume is created.
type PlacementSpec struct {
	// Location names where the resource is created; omitted, the workspace default. Fixed once created.
	Location string `yaml:"location,omitempty" json:"location,omitempty"`
}

// DatabasePlacementSpec also accepts the old string form (placement: dedicated) as instance.
type DatabasePlacementSpec struct {
	// Location names where the database is created; omitted, the workspace default. Fixed once created.
	Location string `yaml:"location,omitempty" json:"location,omitempty"`

	legacyInstance string
}

type SourceSpec struct {
	// Git is the repository URL (https or ssh). Required.
	Git string `yaml:"git" json:"git"`
	// Ref is the branch, tag or commit to build. Empty builds the repository's default branch.
	Ref string `yaml:"ref,omitempty" json:"ref,omitempty"`
	// BuildMethod empty means auto: Dockerfile if present, else buildpacks.
	BuildMethod string `yaml:"buildMethod,omitempty" json:"buildMethod,omitempty"`
	// Builder and Buildpacks apply to the buildpack path only.
	Builder    string   `yaml:"builder,omitempty" json:"builder,omitempty"`
	Buildpacks []string `yaml:"buildpacks,omitempty" json:"buildpacks,omitempty"`
	// BuildEnv is for the build only, not the running container; runtime env belongs in spec.env.
	BuildEnv map[string]string `yaml:"buildEnv,omitempty" json:"buildEnv,omitempty"`
	// Repository names a GitRepository in the bundle or one already in the workspace.
	Repository string `yaml:"repository,omitempty" json:"repository,omitempty"`
}

// PortSpec exposure knobs are orthogonal: externalAccess is a public HTTPS URL, publish binds a raw node port.
type PortSpec struct {
	// Name lets a Route target the port by name instead of its number.
	Name      string `yaml:"name,omitempty" json:"name,omitempty"`
	Container int    `yaml:"container" json:"container"`
	Protocol  string `yaml:"protocol,omitempty" json:"protocol,omitempty"` // tcp|udp (default tcp)
	Scheme    string `yaml:"scheme,omitempty" json:"scheme,omitempty"`     // http|https (default http)
	// ExternalAccess requires the base domain to be configured platform-wide.
	ExternalAccess bool `yaml:"externalAccess,omitempty" json:"externalAccess,omitempty"`
	// HostPort 0 auto-allocates from the pool.
	Publish  bool `yaml:"publish,omitempty" json:"publish,omitempty"`
	HostPort int  `yaml:"hostPort,omitempty" json:"hostPort,omitempty"`
}

// MountSpec attaches a managed Volume into the container filesystem.
type MountSpec struct {
	Volume string `yaml:"volume,omitempty" json:"volume,omitempty"`
	Config string `yaml:"config,omitempty" json:"config,omitempty"`
	// Key makes Path that file's exact location; empty projects every key under Path as a directory.
	Key      string `yaml:"key,omitempty" json:"key,omitempty"`
	Path     string `yaml:"path" json:"path"`
	Mode     string `yaml:"mode,omitempty" json:"mode,omitempty"`
	ReadOnly bool   `yaml:"readOnly,omitempty" json:"readOnly,omitempty"`
}

// SecuritySpec omitted fields mean the container default, so removing one converges.
type SecuritySpec struct {
	// RunAsUser empty keeps the image user; restricted workspaces must use a non-root numeric uid or apply refuses.
	RunAsUser string `yaml:"runAsUser,omitempty" json:"runAsUser,omitempty"`
	// ReadOnlyRootFilesystem leaves volumes writable, so paths like /tmp need a volume.
	ReadOnlyRootFilesystem bool `yaml:"readOnlyRootFilesystem,omitempty" json:"readOnlyRootFilesystem,omitempty"`
	// NoNewPrivileges false is refused under the restricted profile rather than silently overridden.
	NoNewPrivileges *bool `yaml:"noNewPrivileges,omitempty" json:"noNewPrivileges,omitempty"`
	// Capabilities adds Linux capabilities beyond Docker's default set and drops any from it.
	Capabilities *CapabilitiesSpec `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	// Devices are host device nodes exposed to the container, e.g. ["/dev/net/tun"].
	Devices []string `yaml:"devices,omitempty" json:"devices,omitempty"`
	// Deprecated: use capabilities.add. Still accepted; setting both is an error.
	DeprecatedAddCapabilities []string `yaml:"addCapabilities,omitempty" json:"addCapabilities,omitempty"`
}

// CapabilitiesSpec changes the Linux capabilities a container starts with. The CAP_ prefix is optional.
type CapabilitiesSpec struct {
	// Add refuses anything off the allow-list on apply rather than dropping it.
	Add []string `yaml:"add,omitempty" json:"add,omitempty"`
	// Drop removes capabilities from the default set. ALL removes every one, leaving only what add grants.
	Drop []string `yaml:"drop,omitempty" json:"drop,omitempty"`
}

// ResourceSpec caps memory/CPU and requests GPUs. Empty/zero means unlimited/none.
type ResourceSpec struct {
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"` // e.g. "512Mi"
	CPU    string `yaml:"cpu,omitempty" json:"cpu,omitempty"`       // e.g. "0.5"
	// GPUKind narrows the request to a vendor/model ("nvidia", "NVIDIA A100-...").
	GPU     int    `yaml:"gpu,omitempty" json:"gpu,omitempty"`
	GPUKind string `yaml:"gpuKind,omitempty" json:"gpuKind,omitempty"`
}

// StackSpec groups applications into one logical unit / network.
type StackSpec struct {
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Placement is where the stack is created. Member applications that name no location follow it.
	Placement *PlacementSpec `yaml:"placement,omitempty" json:"placement,omitempty"`
	// Env is shared by every member application, injected on its next deploy; a member's own variable with the same key wins. Values interpolate like application env.
	Env       map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	SecretEnv []string          `yaml:"secretEnv,omitempty" json:"secretEnv,omitempty"` // keys stored encrypted; vault references need none, apply-resolved credentials are added
}

// DatabaseSpec requests a logical database on a DatabaseInstance.
type DatabaseSpec struct {
	Engine  string `yaml:"engine" json:"engine"`                       // postgres|mysql|mariadb|redis|mongodb|libsql
	Version string `yaml:"version,omitempty" json:"version,omitempty"` // e.g. "16-alpine"
	// Instance is auto (reuse a compatible one, else provision), dedicated (always provision) or shared (must exist).
	Instance string `yaml:"instance,omitempty" json:"instance,omitempty"`
	// Placement's string form (placement: dedicated) is the deprecated spelling of instance.
	Placement *DatabasePlacementSpec `yaml:"placement,omitempty" json:"placement,omitempty"`
	// Resources give the database its own instance, since a reused one runs with another's size; omitted keeps limits.
	Resources *DatabaseResourcesSpec `yaml:"resources,omitempty" json:"resources,omitempty"`
	// ReadOnlyUser (PostgreSQL) grants SELECT on all tables; omitted keeps what it has, false removes it.
	ReadOnlyUser *bool `yaml:"readOnlyUser,omitempty" json:"readOnlyUser,omitempty"`
}

// DatabaseResourcesSpec memory accepts Ki/Mi/Gi, CPU is cores ("0.5"); "0" removes a limit.
type DatabaseResourcesSpec struct {
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"`
	CPU    string `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	// Size names a database size the platform offers (Enterprise), instead of memory and cpu.
	Size string `yaml:"size,omitempty" json:"size,omitempty"`
}

// VolumeSpec declares persistent storage.
type VolumeSpec struct {
	Size string `yaml:"size,omitempty" json:"size,omitempty"` // e.g. "5Gi" (0/empty = unbounded)
	// StorageClass is immutable, so apply refuses an edit; omit it to keep the manifest portable across installs.
	StorageClass string `yaml:"storageClass,omitempty" json:"storageClass,omitempty"`
	// Placement is where the volume is created.
	Placement *PlacementSpec `yaml:"placement,omitempty" json:"placement,omitempty"`
}

// SizeBytes parses the declared capacity (e.g. "5Gi") into bytes. Empty or "0" means unbounded.
func (v *VolumeSpec) SizeBytes() (int64, error) {
	rs := ResourceSpec{Memory: v.Size}
	return rs.MemoryBytes()
}

type RouteSpec struct {
	Hosts []string `yaml:"hosts" json:"hosts"`
	App   string   `yaml:"app" json:"app"` // target application name
	// Port is a container port number, or the name of one of the app's ports.
	Port RoutePort `yaml:"port,omitempty" json:"port,omitzero"`
	Path string    `yaml:"path,omitempty" json:"path,omitempty"`
	// TLS omitted serves HTTPS with a certificate issued by ACME.
	TLS *RouteTLSSpec `yaml:"tls,omitempty" json:"tls,omitempty"`
	// Deprecated: use tls.certificate. Still accepted; setting both is an error.
	DeprecatedCertificate string `yaml:"certificate,omitempty" json:"certificate,omitempty"`
	// Deprecated: use tls.provider. Still accepted; setting both is an error.
	DeprecatedTLSProvider string   `yaml:"tlsProvider,omitempty" json:"tlsProvider,omitempty"`
	Rewrite               string   `yaml:"rewrite,omitempty" json:"rewrite,omitempty"`
	Methods               []string `yaml:"methods,omitempty" json:"methods,omitempty"`
	// AdvancedConfig is raw Goma route YAML that SUPERSEDES the structured fields above.
	AdvancedConfig string `yaml:"advancedConfig,omitempty" json:"advancedConfig,omitempty"`
	// Security carries the gateway's per-route switches.
	Security *RouteSecuritySpec `yaml:"security,omitempty" json:"security,omitempty"`
	// Maintenance omitted means serving, so removing the block resumes traffic.
	Maintenance *RouteMaintenanceSpec `yaml:"maintenance,omitempty" json:"maintenance,omitempty"`
	Middlewares []string              `yaml:"middlewares,omitempty" json:"middlewares,omitempty"`
}

// RoutePort also reads the name of a port the target application declares.
type RoutePort struct {
	Number int
	Name   string
}

// RouteTLSSpec also accepts the old string form (tls: acme|custom|off).
type RouteTLSSpec struct {
	// Enabled false serves plain HTTP; omitted means true.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Provider is the gateway certificate provider that issues the ACME certificate; omitted, the gateway default.
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	// Certificate serves an imported certificate instead of an ACME one.
	Certificate string `yaml:"certificate,omitempty" json:"certificate,omitempty"`

	legacyMode string
}

type RouteSecuritySpec struct {
	ExploitProtection bool `yaml:"exploitProtection,omitempty" json:"exploitProtection,omitempty"`
}

// RouteMaintenanceSpec unset fields keep the gateway defaults (503 and a short notice).
type RouteMaintenanceSpec struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// StatusCode must be 4xx or 5xx: a 2xx parked route reads as healthy to monitors and search engines.
	StatusCode int    `yaml:"statusCode,omitempty" json:"statusCode,omitempty"`
	Message    string `yaml:"message,omitempty" json:"message,omitempty"`
}

// MiddlewareSpec Rule is the gateway type's own configuration, passed through untranslated.
type MiddlewareSpec struct {
	Type string `yaml:"type" json:"type"`
	// Paths empty applies the middleware to everything the route serves.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	// Rule is the type's configuration, passed through to the gateway.
	Rule   map[string]any `yaml:"rule,omitempty" json:"rule,omitempty"`
	RuleFP string         `yaml:"-" json:"-"`
}

// CronJobSpec runs on the app's active release unless Image overrides it.
type CronJobSpec struct {
	// App is immutable: moving a schedule to another app is a delete and a create.
	App string `yaml:"app" json:"app"`
	// Schedule is a standard 5-field cron expression, evaluated in UTC.
	Schedule string `yaml:"schedule" json:"schedule"`
	// Command is the command to run, as a list of arguments (no shell).
	Command []string `yaml:"command" json:"command"`
	// Entrypoint overrides the image's entrypoint.
	Entrypoint []string `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
	// Image runs a different image in the app's runtime context; omitted runs the app's active release.
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
	// Registry names the Registry credential that pulls Image. Only valid with image.
	Registry string `yaml:"registry,omitempty" json:"registry,omitempty"`
	// TimeoutSeconds stops a run that takes longer; 0 uses the platform default.
	TimeoutSeconds int `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	// ConcurrencyPolicy is allow (default), forbid (skip the fire) or replace (cancel the running one).
	ConcurrencyPolicy string `yaml:"concurrencyPolicy,omitempty" json:"concurrencyPolicy,omitempty"`
	// HistoryLimit is how many finished runs to keep; 0 keeps the default 20.
	HistoryLimit int `yaml:"historyLimit,omitempty" json:"historyLimit,omitempty"`
	// Suspend omitted leaves the live value alone, so a console pause survives a sync.
	Suspend *bool `yaml:"suspend,omitempty" json:"suspend,omitempty"`
	// Security pins the account runs use; omitted inherits the app's when each run fires.
	Security *SecuritySpec `yaml:"security,omitempty" json:"security,omitempty"`
}

// Job run policies.
const (
	// RunOnChange fingerprint covers command, entrypoint, image, registry, runAsUser and timeout.
	RunOnChange = "onChange"
	// RunOnce runs the Job exactly once per name; later spec edits are refused. Rename it to run again.
	RunOnce = "once"
	// RunOnRelease fires however the deploy started (GitOps, console, pipeline, rollback).
	RunOnRelease = "onRelease"
)

// JobSpec never runs twice for the same commit; a failed run is not retried by the next sync.
type JobSpec struct {
	// App is the Application the command runs in. Immutable.
	App string `yaml:"app" json:"app"`
	// Command is the command to run, as a list of arguments (no shell).
	Command []string `yaml:"command" json:"command"`
	// Entrypoint overrides the image's entrypoint.
	Entrypoint []string `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
	// Image runs a different image in the app's runtime context; omitted runs the app's active release.
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
	// Registry names the Registry credential that pulls Image. Only valid with image.
	Registry string `yaml:"registry,omitempty" json:"registry,omitempty"`
	// TimeoutSeconds stops a run that takes longer; 0 uses the platform default.
	TimeoutSeconds int    `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	RunPolicy      string `yaml:"runPolicy,omitempty" json:"runPolicy,omitempty"`
	// WaitForDeploy (onChange only) runs on the new release; a failed deploy marks the run skipped.
	WaitForDeploy *bool `yaml:"waitForDeploy,omitempty" json:"waitForDeploy,omitempty"`
	// HistoryLimit is how many finished runs to keep; 0 keeps the default 20.
	HistoryLimit int `yaml:"historyLimit,omitempty" json:"historyLimit,omitempty"`
	// BackoffLimit 0 never retries; retries start a new run, a sync never does.
	BackoffLimit int `yaml:"backoffLimit,omitempty" json:"backoffLimit,omitempty"`
	// Security pins the account runs use; omitted inherits the app's.
	Security *SecuritySpec `yaml:"security,omitempty" json:"security,omitempty"`
	// SpecFP is derived state filled on the live side, never serialized.
	SpecFP string `yaml:"-" json:"-"`
}

// DomainSpec wildcard also covers *.name; ownership verification is a runtime action.
type DomainSpec struct {
	TLS      string `yaml:"tls,omitempty" json:"tls,omitempty"` // acme|custom (default acme)
	Wildcard bool   `yaml:"wildcard,omitempty" json:"wildcard,omitempty"`
}

// GitSourceSpec webhook secret is generated on create and never declared.
type GitSourceSpec struct {
	Repo       string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Repository string `yaml:"repository,omitempty" json:"repository,omitempty"`
	Ref        string `yaml:"ref,omitempty" json:"ref,omitempty"`               // branch, tag or commit (default main)
	Path       string `yaml:"path,omitempty" json:"path,omitempty"`             // manifest directory (default .)
	SyncPolicy string `yaml:"syncPolicy,omitempty" json:"syncPolicy,omitempty"` // manual|auto (default manual)
	Prune      bool   `yaml:"prune,omitempty" json:"prune,omitempty"`
	SelfHeal   bool   `yaml:"selfHeal,omitempty" json:"selfHeal,omitempty"`
	AllowEmpty bool   `yaml:"allowEmpty,omitempty" json:"allowEmpty,omitempty"`
	// RequireSealedSecrets refuses a sync whose manifests carry a plaintext Secret value.
	RequireSealedSecrets bool `yaml:"requireSealedSecrets,omitempty" json:"requireSealedSecrets,omitempty"`
	// DeletePolicy orphan (default) keeps a mistaken prune harmless; cascade tears resources down.
	DeletePolicy string `yaml:"deletePolicy,omitempty" json:"deletePolicy,omitempty"`
}

// GitRepositorySpec is safe to commit when the SecretRef vault entry is sealed.
type GitRepositorySpec struct {
	URL       string `yaml:"url" json:"url"`
	Auth      string `yaml:"auth,omitempty" json:"auth,omitempty"` // public|token|ssh (default token with a secretRef, else public)
	Username  string `yaml:"username,omitempty" json:"username,omitempty"`
	SecretRef string `yaml:"secretRef,omitempty" json:"secretRef,omitempty"`
}

// GitSource delete policies.
const (
	DeletePolicyOrphan  = "orphan"
	DeletePolicyCascade = "cascade"
)

// RegistrySpec Password is write-only and never in a plan; rotation converges via PasswordFP.
type RegistrySpec struct {
	Server   string `yaml:"server,omitempty" json:"server,omitempty"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	// PasswordFP is derived state, so a Marshal->Parse round trip (GitOps canonicalization) recomputes it.
	PasswordFP string `yaml:"-" json:"-"`
}

// SecretSpec generation options mirror the console generator so both mean the same thing.
type SecretSpec struct {
	Value    string `yaml:"value,omitempty" json:"value,omitempty"`
	Generate bool   `yaml:"generate,omitempty" json:"generate,omitempty"`
	Length   int    `yaml:"length,omitempty" json:"length,omitempty"`
	Symbols  *bool  `yaml:"symbols,omitempty" json:"symbols,omitempty"`
	// Asking for a minimum of symbols implies symbols.
	MinNumbers int `yaml:"minNumbers,omitempty" json:"minNumbers,omitempty"`
	MinSpecial int `yaml:"minSpecial,omitempty" json:"minSpecial,omitempty"`
}

// SealedSecretSpec opens only in the workspace and name it was sealed for; compared by fingerprint.
type SealedSecretSpec struct {
	// Value is the output of `miabi secrets seal`: sealed:v1:<keyVersion>:<data>. Never plaintext.
	Value string `yaml:"value" json:"value"`
	// SealedFP is derived state stamped on both sides of the diff, never serialized.
	SealedFP string `yaml:"-" json:"-"`
}

// IsVaultSecret: Secret and SealedSecret share one name space, so a name is either, never both.
func IsVaultSecret(k Kind) bool { return k == KindSecret || k == KindSealedSecret }

func (s SecretSpec) WantSymbols() bool {
	if s.Symbols != nil {
		return *s.Symbols
	}
	return s.MinSpecial > 0
}

// ConfigSpec values are interpolated like env, so a config can carry a rendered database password.
type ConfigSpec struct {
	Data map[string]string `yaml:"data" json:"data"`
	// Mode is the default octal file mode applied to every key ("0644" when empty).
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
	// Sensitive redacts content in API responses and diffs it by digest only.
	Sensitive bool `yaml:"sensitive,omitempty" json:"sensitive,omitempty"`
	// Delimiters must be exactly two distinct entries.
	Delimiters []string `yaml:"delimiters,omitempty" json:"delimiters,omitempty"`
	// DigestFP is derived state filled on both sides of the diff, never serialized.
	DigestFP string `yaml:"-" json:"-"`
}

type ProjectSpec struct {
	Description string     `yaml:"description,omitempty" json:"description,omitempty"`
	Resources   []Resource `yaml:"-" json:"resources,omitempty"`
}
