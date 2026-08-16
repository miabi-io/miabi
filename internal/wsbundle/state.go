// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package wsbundle

import (
	"errors"
	"fmt"
	"time"
)

// StateSchema is the state document's version. A restore refuses a schema it
// does not know rather than guessing at fields.
const StateSchema = 1

// State is a workspace's whole configuration, by natural key. Nothing here carries a primary key:
// across two installs a uint id is meaningless, so every reference is a name and restore rebuilds
// the graph from those names. It holds plaintext secrets, so it only exists sealed or in memory.
type State struct {
	Schema int `json:"schema"`

	// Source describes where the bundle came from, for the restore report. It is
	// never required to match anything on the target.
	Source Source `json:"source"`

	Workspace Workspace `json:"workspace"`

	Registries   []Registry         `json:"registries,omitempty"`
	GitRepos     []GitRepository    `json:"git_repositories,omitempty"`
	DNSProviders []DNSProvider      `json:"dns_providers,omitempty"`
	Networks     []Network          `json:"networks,omitempty"`
	Secrets      []Secret           `json:"secrets,omitempty"`
	Volumes      []Volume           `json:"volumes,omitempty"`
	Stacks       []Stack            `json:"stacks,omitempty"`
	Databases    []DatabaseInstance `json:"databases,omitempty"`
	Certificates []Certificate      `json:"certificates,omitempty"`
	Apps         []Application      `json:"apps,omitempty"`
	CronJobs     []CronJob          `json:"cron_jobs,omitempty"`
	Middlewares  []Middleware       `json:"middlewares,omitempty"`
	Routes       []Route            `json:"routes,omitempty"`
	Domains      []Domain           `json:"domains,omitempty"`
	Environments []Environment      `json:"environments,omitempty"`
	Pipelines    []Pipeline         `json:"pipelines,omitempty"`
	GitSources   []GitSource        `json:"gitops_sources,omitempty"`
	Members      []Member           `json:"members,omitempty"`
}

// Source is the bundle's provenance.
type Source struct {
	InstallID    string    `json:"install_id,omitempty"`
	MiabiVersion string    `json:"miabi_version,omitempty"`
	ExportedAt   time.Time `json:"exported_at"`
}

// Workspace is the workspace record itself. Plan, quota and privilege are deliberately absent:
// they are the target platform's decisions about a tenant, not the tenant's own state, and a
// bundle that carried them would let an import grant itself capacity or privilege.
type Workspace struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// Registry is a container-registry credential (secret in plaintext, inside the
// sealed file), so a private-image app still pulls on the target.
type Registry struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Server      string `json:"server,omitempty"`
	Username    string `json:"username,omitempty"`
	Secret      string `json:"secret,omitempty"`
}

// GitRepository is a Git credential, so a git-source app still clones.
type GitRepository struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	URL         string `json:"url"`
	AuthType    string `json:"auth_type,omitempty"`
	Username    string `json:"username,omitempty"`
	Secret      string `json:"secret,omitempty"`
}

// DNSProvider is a connection to a DNS host. Credentials travel because what they authorize —
// publishing an ownership TXT record, answering a DNS-01 challenge — is exactly what the target
// must do before a migrated domain serves anything.
type DNSProvider struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Type        string `json:"type"` // cloudflare | route53 | digitalocean
	// Credentials is the provider's credential blob as JSON, the same shape the
	// platform stores encrypted.
	Credentials string `json:"credentials,omitempty"`
}

// Certificate is a TLS certificate the workspace owns. Only uploaded certificates carry their
// material; a Miabi-issued one carries its declaration and nothing else, since its key was minted
// for a host that still resolves to the source. Reissuing is a cutover step, not an import step.
type Certificate struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	Source      string   `json:"source,omitempty"` // imported | acme
	DNSNames    []string `json:"dns_names,omitempty"`
	// CertPEM and KeyPEM are the material, present only for an imported cert.
	CertPEM string `json:"cert_pem,omitempty"`
	KeyPEM  string `json:"key_pem,omitempty"`
	// DNSProvider and AutoRenew describe how a managed cert was issued, so the
	// report can say what to re-issue and against which connection.
	DNSProvider string `json:"dns_provider,omitempty"`
	AutoRenew   bool   `json:"auto_renew,omitempty"`
}

// Middleware is a gateway middleware routes attach by name. Its rule may hold
// secret fields (a basicAuth password, a forwardAuth shared secret) — which is
// why, like everything else here, it only exists inside the sealed state file.
type Middleware struct {
	Name        string         `json:"name"`
	DisplayName string         `json:"display_name,omitempty"`
	Type        string         `json:"type"`
	Paths       []string       `json:"paths,omitempty"`
	Rule        map[string]any `json:"rule,omitempty"`
}

// CronJob is a scheduled command in an application's runtime context. The
// schedule travels; the runs it produced do not.
type CronJob struct {
	Name              string   `json:"name"`
	App               string   `json:"app"`
	Schedule          string   `json:"schedule"`
	Command           []string `json:"command,omitempty"`
	Entrypoint        []string `json:"entrypoint,omitempty"`
	Image             string   `json:"image,omitempty"`
	Registry          string   `json:"registry,omitempty"`
	TimeoutSecs       int      `json:"timeout_secs,omitempty"`
	Enabled           bool     `json:"enabled"`
	ConcurrencyPolicy string   `json:"concurrency_policy,omitempty"`
	HistoryLimit      int      `json:"history_limit,omitempty"`
}

// Environment is a promotion stage. Approvals recorded against releases do not
// travel — they are decisions made about a release on the platform that held it.
type Environment struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name,omitempty"`
	Description       string `json:"description,omitempty"`
	Rank              int    `json:"rank,omitempty"`
	RequiredApprovals int    `json:"required_approvals,omitempty"`
	GitSource         string `json:"gitops_source,omitempty"`
}

// Pipeline is a pipeline-as-code definition. Its runs, logs and built images
// stay behind: a run is a record of something that happened on the platform that
// ran it, and the definition is what reproduces it.
type Pipeline struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	App         string `json:"app,omitempty"`
	Spec        string `json:"spec"`
	Enabled     bool   `json:"enabled"`
	// Source is manual or repo. A repo-owned spec is carried as it was last read,
	// and the target re-reads it from the repository at the next run.
	Source     string `json:"source,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	SourceRef  string `json:"source_ref,omitempty"`
}

// GitSource is a GitOps connection: the repository of manifests, not a fork of it. The manifests
// are in Git, so what travels is how to reach it and how to reconcile it. The webhook secret does
// not travel — carrying it would let the same push drive two installs.
type GitSource struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name,omitempty"`
	RepoURL       string `json:"repo_url"`
	Ref           string `json:"ref,omitempty"`
	Path          string `json:"path,omitempty"`
	GitRepository string `json:"git_repository,omitempty"` // stored credential, by name
	SyncPolicy    string `json:"sync_policy,omitempty"`    // manual | auto
	Prune         bool   `json:"prune,omitempty"`
	SelfHeal      bool   `json:"self_heal,omitempty"`
	AllowEmpty    bool   `json:"allow_empty,omitempty"`
	// LastSyncedCommit lets the target decide "already at HEAD" instead of
	// redeploying everything on its first reconcile.
	LastSyncedCommit string `json:"last_synced_commit,omitempty"`
}

// Network is a workspace-owned Docker network. The Docker name is not carried:
// it is platform-managed and globally unique, so the target mints its own.
type Network struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Driver      string `json:"driver,omitempty"`
	Internal    bool   `json:"internal,omitempty"`
}

// Secret is one vault entry with its value.
type Secret struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name,omitempty"`
	Description string            `json:"description,omitempty"`
	Value       string            `json:"value"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Volume is a managed volume's declaration. Its contents travel separately, as a
// data artifact named after Name.
type Volume struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name,omitempty"`
	SizeBytes   int64             `json:"size_bytes,omitempty"`
	Driver      string            `json:"driver,omitempty"`
	AccessMode  string            `json:"access_mode,omitempty"`
	HostPath    string            `json:"host_path,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Stack groups applications and carries the environment shared by its members.
type Stack struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	// Env is injected into every member application's containers. Secret values
	// are in plaintext here, inside the sealed file, as everywhere else.
	Env         []EnvVar          `json:"env,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// DatabaseInstance is a managed database server and the logical databases on it. Credentials
// deliberately do NOT travel: data crosses as a logical dump restored into a server the target
// initialized itself. EnvPrefix and App do travel, so consuming apps are re-injected on restore.
type DatabaseInstance struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name,omitempty"`
	Engine      string            `json:"engine"`
	Version     string            `json:"version,omitempty"`
	VolumeSize  int64             `json:"volume_size_bytes,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Databases   []LogicalDatabase `json:"logical_databases,omitempty"`
}

// LogicalDatabase is one named database on an instance and the app it belongs to.
type LogicalDatabase struct {
	Name      string            `json:"name"`
	EnvPrefix string            `json:"env_prefix,omitempty"`
	App       string            `json:"app,omitempty"` // owning application, by name
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Application is a workload's full spec.
type Application struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	SourceType  string `json:"source_type,omitempty"` // image | git
	Icon        string `json:"icon,omitempty"`

	Image string `json:"image,omitempty"`
	Tag   string `json:"tag,omitempty"`

	GitRepo     string            `json:"git_repo,omitempty"`
	GitRef      string            `json:"git_ref,omitempty"`
	BuildMethod string            `json:"build_method,omitempty"`
	Builder     string            `json:"builder,omitempty"`
	Buildpacks  []string          `json:"buildpacks,omitempty"`
	BuildEnv    map[string]string `json:"build_env,omitempty"`

	// Registry and GitRepository name the stored credentials this app uses.
	Registry      string `json:"registry,omitempty"`
	GitRepository string `json:"git_repository,omitempty"`

	Stack    string   `json:"stack,omitempty"`
	Networks []string `json:"networks,omitempty"`

	Command []string `json:"command,omitempty"`
	Port    int      `json:"port,omitempty"`
	Ports   []Port   `json:"ports,omitempty"`
	Mounts  []Mount  `json:"mounts,omitempty"`
	Env     []EnvVar `json:"env,omitempty"`

	MemoryBytes     int64  `json:"memory_bytes,omitempty"`
	NanoCPUs        int64  `json:"nano_cpus,omitempty"`
	GPUCount        int    `json:"gpu_count,omitempty"`
	GPUKind         string `json:"gpu_kind,omitempty"`
	RestartPolicy   string `json:"restart_policy,omitempty"`
	ImagePullPolicy string `json:"image_pull_policy,omitempty"`

	RuntimeKind          string   `json:"runtime_kind,omitempty"`
	Replicas             int      `json:"replicas,omitempty"`
	PlacementConstraints []string `json:"placement_constraints,omitempty"`

	HealthcheckType               string `json:"healthcheck_type,omitempty"`
	HealthcheckHTTPPath           string `json:"healthcheck_http_path,omitempty"`
	HealthcheckPort               int    `json:"healthcheck_port,omitempty"`
	HealthcheckCommand            string `json:"healthcheck_command,omitempty"`
	HealthcheckIntervalSeconds    int    `json:"healthcheck_interval_seconds,omitempty"`
	HealthcheckTimeoutSeconds     int    `json:"healthcheck_timeout_seconds,omitempty"`
	HealthcheckRetries            int    `json:"healthcheck_retries,omitempty"`
	HealthcheckStartPeriodSeconds int    `json:"healthcheck_start_period_seconds,omitempty"`

	DeployStrategy string `json:"deploy_strategy,omitempty"`

	ContainerLabels map[string]string `json:"container_labels,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
}

// Port is a container port the app exposes.
type Port struct {
	Container int    `json:"container"`
	Protocol  string `json:"protocol,omitempty"`
	Scheme    string `json:"scheme,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Mount attaches a managed volume by the volume's Miabi name. Privileged host-path binds are not
// carried: their source is an allow-listed preset on the node that granted it, and it may not
// exist — or may mean something else — on the target.
type Mount struct {
	Volume   string `json:"volume"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// EnvVar is one environment variable. Secret values are in plaintext here, inside
// the sealed file, because the target re-encrypts under its own key.
type EnvVar struct {
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	IsSecret bool   `json:"is_secret,omitempty"`
}

// Route is an HTTP routing rule bound to an app by name.
type Route struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	App         string   `json:"app"`
	Hosts       []string `json:"hosts,omitempty"`
	Path        string   `json:"path,omitempty"`
	Methods     []string `json:"methods,omitempty"`
	Middlewares []string `json:"middlewares,omitempty"`
	Rewrite     string   `json:"rewrite,omitempty"`
	TargetPort  int      `json:"target_port,omitempty"`
	TLSMode     string   `json:"tls_mode,omitempty"`
	// Certificate names the uploaded certificate a custom-TLS route serves, by
	// its Miabi name.
	Certificate       string            `json:"certificate,omitempty"`
	Enabled           bool              `json:"enabled"`
	ExploitProtection bool              `json:"exploit_protection,omitempty"`
	Maintenance       *RouteMaintenance `json:"maintenance,omitempty"`
}

type RouteMaintenance struct {
	Enabled    bool   `json:"enabled"`
	StatusCode int    `json:"status_code,omitempty"`
	Message    string `json:"message,omitempty"`
}

// Domain is an owned hostname. Ownership is not carried: a domain is verified
// against DNS, and DNS on the target points wherever it points — so a restored
// domain starts unverified and re-proves itself.
type Domain struct {
	Name    string `json:"name"`
	TLSMode string `json:"tls_mode,omitempty"`
	// DNSProvider names the connection that automates this domain's records, so
	// the target can prove ownership without an operator pasting a TXT record.
	DNSProvider string `json:"dns_provider,omitempty"`
	Wildcard    bool   `json:"wildcard,omitempty"`
}

// Member is a workspace membership, by email. Users are instance-global: the
// target may know this person, or may not, and either way it authenticates them
// itself. Password hashes, sessions, 2FA secrets and token values never travel.
type Member struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	Owner bool   `json:"owner,omitempty"`
}

// Validate checks an opened state document is one this build understands.
func (s *State) Validate() error {
	if s.Schema != StateSchema {
		return fmt.Errorf("bundle state schema %d is not supported by this build (expected %d)", s.Schema, StateSchema)
	}
	if s.Workspace.Name == "" {
		return errors.New("bundle state names no workspace")
	}
	return nil
}
