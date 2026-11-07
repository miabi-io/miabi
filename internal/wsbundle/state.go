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

package wsbundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// StateSchema history: 2 configs, 3 "none" healthcheck disables, 4 sealing keys, 5 version policy, 6 db source.
const StateSchema = 6

// HealthcheckNoneDisables is the first schema whose "none" healthcheck disables the image's own check.
const HealthcheckNoneDisables = 3

// Every version in range differs only by fields absent in older ones, which decode to zero.
const MinStateSchema = 1

// Every reference is a name, since uint ids are meaningless across installs; it only exists sealed or in memory.
type State struct {
	Schema int `json:"schema"`

	Source Source `json:"source"`

	Workspace Workspace `json:"workspace"`

	Registries   []Registry      `json:"registries,omitempty"`
	GitRepos     []GitRepository `json:"git_repositories,omitempty"`
	DNSProviders []DNSProvider   `json:"dns_providers,omitempty"`
	Networks     []Network       `json:"networks,omitempty"`
	// SealingKeys travel so values sealed to the source workspace keep opening on the target.
	SealingKeys  []SealingKey       `json:"sealing_keys,omitempty"`
	Secrets      []Secret           `json:"secrets,omitempty"`
	Configs      []Config           `json:"configs,omitempty"`
	Volumes      []Volume           `json:"volumes,omitempty"`
	Stacks       []Stack            `json:"stacks,omitempty"`
	Databases    []DatabaseInstance `json:"databases,omitempty"`
	Certificates []Certificate      `json:"certificates,omitempty"`
	Apps         []Application      `json:"apps,omitempty"`
	CronJobs     []CronJob          `json:"cron_jobs,omitempty"`
	// Carried so a restored GitOps source re-syncs them as unchanged rather than running every Job again.
	JobDefinitions []JobDefinition `json:"job_definitions,omitempty"`
	Middlewares    []Middleware    `json:"middlewares,omitempty"`
	Routes         []Route         `json:"routes,omitempty"`
	Domains        []Domain        `json:"domains,omitempty"`
	Environments   []Environment   `json:"environments,omitempty"`
	Pipelines      []Pipeline      `json:"pipelines,omitempty"`
	GitSources     []GitSource     `json:"gitops_sources,omitempty"`
	Members        []Member        `json:"members,omitempty"`
}

// Source is the bundle's provenance.
type Source struct {
	InstallID    string    `json:"install_id,omitempty"`
	MiabiVersion string    `json:"miabi_version,omitempty"`
	ExportedAt   time.Time `json:"exported_at"`
}

// Plan, quota and privilege are absent: carrying them would let an import grant itself capacity or privilege.
type Workspace struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
}

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

// Credentials travel because the target must publish ownership TXT records and answer DNS-01 before serving.
type DNSProvider struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Type        string `json:"type"` // cloudflare | route53 | digitalocean
	Credentials string `json:"credentials,omitempty"`
}

// Only uploaded certs carry material; a managed cert's key belongs to a host still resolving to the source.
type Certificate struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	Source      string   `json:"source,omitempty"` // imported | acme
	DNSNames    []string `json:"dns_names,omitempty"`
	// CertPEM and KeyPEM are the material, present only for an imported cert.
	CertPEM     string `json:"cert_pem,omitempty"`
	KeyPEM      string `json:"key_pem,omitempty"`
	DNSProvider string `json:"dns_provider,omitempty"`
	AutoRenew   bool   `json:"auto_renew,omitempty"`
}

// The rule may hold secret fields, which is why it only exists inside the sealed state file.
type Middleware struct {
	Name        string         `json:"name"`
	DisplayName string         `json:"display_name,omitempty"`
	Type        string         `json:"type"`
	Paths       []string       `json:"paths,omitempty"`
	Rule        map[string]any `json:"rule,omitempty"`
}

type CronJob struct {
	Name              string            `json:"name"`
	App               string            `json:"app"`
	Schedule          string            `json:"schedule"`
	Command           []string          `json:"command,omitempty"`
	Entrypoint        []string          `json:"entrypoint,omitempty"`
	Image             string            `json:"image,omitempty"`
	Registry          string            `json:"registry,omitempty"`
	TimeoutSecs       int               `json:"timeout_secs,omitempty"`
	Enabled           bool              `json:"enabled"`
	ConcurrencyPolicy string            `json:"concurrency_policy,omitempty"`
	HistoryLimit      int               `json:"history_limit,omitempty"`
	DisplayName       string            `json:"display_name,omitempty"`
	RunAsUser         string            `json:"run_as_user,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
}

// JobDefinition is a declared Job. Its fingerprint travels with it; its runs do not.
type JobDefinition struct {
	Name          string            `json:"name"`
	App           string            `json:"app"`
	Command       []string          `json:"command,omitempty"`
	Entrypoint    []string          `json:"entrypoint,omitempty"`
	Image         string            `json:"image,omitempty"`
	Registry      string            `json:"registry,omitempty"`
	RunAsUser     string            `json:"run_as_user,omitempty"`
	TimeoutSecs   int               `json:"timeout_secs,omitempty"`
	RunPolicy     string            `json:"run_policy,omitempty"`
	WaitForDeploy bool              `json:"wait_for_deploy"`
	HistoryLimit  int               `json:"history_limit,omitempty"`
	BackoffLimit  int               `json:"backoff_limit,omitempty"`
	SpecHash      string            `json:"spec_hash,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Approvals don't travel: they are decisions made on the platform that held the release.
type Environment struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name,omitempty"`
	Description       string `json:"description,omitempty"`
	Rank              int    `json:"rank,omitempty"`
	RequiredApprovals int    `json:"required_approvals,omitempty"`
	GitSource         string `json:"gitops_source,omitempty"`
}

// Runs, logs and images stay behind; the definition is what reproduces them.
type Pipeline struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	App         string `json:"app,omitempty"`
	Spec        string `json:"spec"`
	Enabled     bool   `json:"enabled"`
	// A repo-owned spec is carried as last read and re-read from the repository at the next run.
	Source     string `json:"source,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	SourceRef  string `json:"source_ref,omitempty"`
	// Lets a restored repository continue its numbering instead of reusing existing tags; opaque here.
	Versioning json.RawMessage `json:"versioning,omitempty"`
}

// The webhook secret does not travel, or the same push would drive two installs.
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
	// RequireSealedSecrets keeps the target refusing plaintext Secret values, as the source did.
	RequireSealedSecrets bool `json:"require_sealed_secrets,omitempty"`
	// Lets the target treat HEAD as already synced instead of redeploying everything.
	LastSyncedCommit string `json:"last_synced_commit,omitempty"`
}

// The Docker name is globally unique and platform-managed, so the target mints its own.
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
	// Lets a GitOps source re-syncing on the target see the secret as unchanged.
	SealedFP         string `json:"sealed_fp,omitempty"`
	SealedKeyVersion int    `json:"sealed_key_version,omitempty"`
}

// SealingKey is a workspace sealing key, private half included.
type SealingKey struct {
	Version   int       `json:"version"`
	Identity  string    `json:"identity"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

type Config struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name,omitempty"`
	Description string            `json:"description,omitempty"`
	Data        map[string]string `json:"data"`
	Mode        string            `json:"mode,omitempty"`
	Sensitive   bool              `json:"sensitive,omitempty"`
	Delimiters  []string          `json:"delimiters,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Contents travel separately as a data artifact named after Name.
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
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name,omitempty"`
	Description string            `json:"description,omitempty"`
	Env         []EnvVar          `json:"env,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Credentials don't travel: data is dumped into a server the target initialized; EnvPrefix/App re-inject apps.
type DatabaseInstance struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Engine      string `json:"engine"`
	Version     string `json:"version,omitempty"`
	VolumeSize  int64  `json:"volume_size_bytes,omitempty"`
	// Restoring onto an install without that class falls back to the default one.
	StorageClass string            `json:"storage_class,omitempty"`
	MemoryBytes  int64             `json:"memory_bytes,omitempty"`
	NanoCPUs     int64             `json:"nano_cpus,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Databases    []LogicalDatabase `json:"logical_databases,omitempty"`
	// Links are the apps the whole instance is linked to (Redis).
	Links []InstanceLink `json:"links,omitempty"`
}

type InstanceLink struct {
	App       string            `json:"app"`
	EnvPrefix string            `json:"env_prefix,omitempty"`
	EnvMap    map[string]string `json:"env_map,omitempty"`
}

// LogicalDatabase is one named database on an instance and the app it belongs to.
type LogicalDatabase struct {
	Name      string            `json:"name"`
	EnvPrefix string            `json:"env_prefix,omitempty"`
	EnvMap    map[string]string `json:"env_map,omitempty"`
	App       string            `json:"app,omitempty"` // owning application, by name
	Metadata  map[string]string `json:"metadata,omitempty"`
	// Credentials never travel, so the read-only user is recreated with new ones.
	ReadOnlyUser bool `json:"readonly_user,omitempty"`
	// Restore must not add connection variables an attach-only app never had.
	AttachOnly bool `json:"attach_only,omitempty"`
	// Restore rewrites env or config values carrying the source connection to the target's.
	Source *Connection `json:"source,omitempty"`
}

// Connection is a logical database's connection details as the source install saw them.
type Connection struct {
	Host             string `json:"host,omitempty"`
	User             string `json:"user,omitempty"`
	Password         string `json:"password,omitempty"`
	URI              string `json:"uri,omitempty"`
	ReadOnlyUser     string `json:"readonly_user,omitempty"`
	ReadOnlyPassword string `json:"readonly_password,omitempty"`
	ReadOnlyURI      string `json:"readonly_uri,omitempty"`
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
	RunAsUser       string `json:"run_as_user,omitempty"`
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

// Host-path binds aren't carried: their allow-listed preset may not exist, or mean something else, on the target.
type Mount struct {
	Volume   string `json:"volume"`
	Config   string `json:"config,omitempty"`
	Key      string `json:"key,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// Plaintext inside the sealed file because the target re-encrypts under its own key.
type EnvVar struct {
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	IsSecret bool   `json:"is_secret,omitempty"`
}

// Route is an HTTP routing rule bound to an app by name.
type Route struct {
	Name              string            `json:"name"`
	DisplayName       string            `json:"display_name,omitempty"`
	App               string            `json:"app"`
	Hosts             []string          `json:"hosts,omitempty"`
	Path              string            `json:"path,omitempty"`
	Methods           []string          `json:"methods,omitempty"`
	Middlewares       []string          `json:"middlewares,omitempty"`
	Rewrite           string            `json:"rewrite,omitempty"`
	TargetPort        int               `json:"target_port,omitempty"`
	TLSMode           string            `json:"tls_mode,omitempty"`
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

// Ownership is not carried: a restored domain starts unverified and re-proves itself against DNS.
type Domain struct {
	Name    string `json:"name"`
	TLSMode string `json:"tls_mode,omitempty"`
	// Lets the target prove ownership without an operator pasting a TXT record.
	DNSProvider string `json:"dns_provider,omitempty"`
	Wildcard    bool   `json:"wildcard,omitempty"`
}

// Users are instance-global; password hashes, sessions, 2FA secrets and tokens never travel.
type Member struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	Owner bool   `json:"owner,omitempty"`
}

// Validate checks an opened state document is one this build understands.
func (s *State) Validate() error {
	// A range: refusing older bundles strands them, and accepting newer ones silently drops what they added.
	if s.Schema < MinStateSchema || s.Schema > StateSchema {
		return fmt.Errorf("bundle state schema %d is not supported by this build (expected %d–%d); "+
			"a bundle from a newer Miabi needs a newer Miabi to restore", s.Schema, MinStateSchema, StateSchema)
	}
	if s.Workspace.Name == "" {
		return errors.New("bundle state names no workspace")
	}
	return nil
}
