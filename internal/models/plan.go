// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Unlimited is the limit value meaning "no cap". Distinct from 0 ("none
// allowed"), so a plan can forbid a resource entirely.
const Unlimited = -1

// UnlimitedPlanName is the name of the built-in plan with no limits and all
// capabilities (seeded by SeedPlans). The platform system workspace is pinned to
// it so platform-managed infrastructure is never constrained by quotas.
const UnlimitedPlanName = "Unlimited"

// SecurityProfile values harden how a workspace's application and job containers
// run. The zero value ("") is treated as SecurityProfileDefault, which keeps the
// Plan model's GORM zero-value-omission invariant intact (see the limits block).
const (
	// SecurityProfileDefault runs containers as the image's default user with no
	// extra hardening — the platform's historical behavior.
	SecurityProfileDefault = "default"
	// SecurityProfileRestricted forces app/job containers to run as the platform
	// non-root UID (UID:0) with no-new-privileges and NET_RAW dropped — like
	// OpenShift's restricted SCC. May break images that require root.
	SecurityProfileRestricted = "restricted"
)

// NormalizeSecurityProfile maps the empty/zero value to the default profile.
func NormalizeSecurityProfile(p string) string {
	if p == SecurityProfileRestricted {
		return SecurityProfileRestricted
	}
	return SecurityProfileDefault
}

// Plan is an admin-defined per-workspace quota + capability template. Numeric
// limits use -1 for unlimited and 0 for none; capability booleans gate features.
type Plan struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	Name        string `json:"name" gorm:"uniqueIndex;not null"`
	Description string `json:"description"`
	IsDefault   bool   `json:"is_default" gorm:"not null;default:false"` // applied to workspaces with no plan
	IsActive    bool   `json:"is_active" gorm:"not null;default:true"`

	// Numeric limits (per workspace unless noted). -1 = unlimited, 0 = none.
	// The DB default is 0 (= the Go zero value) so GORM's zero-value omission on
	// insert is a no-op; an intentional 0 ("none") persists correctly, and -1
	// ("unlimited") is non-zero so it is always written.
	MaxApps              int `json:"max_apps" gorm:"not null;default:0"`
	MaxDatabaseInstances int `json:"max_database_instances" gorm:"not null;default:0"`
	MaxCronJobs          int `json:"max_cron_jobs" gorm:"not null;default:0"`
	MaxVolumes           int `json:"max_volumes" gorm:"not null;default:0"`
	MaxNetworks          int `json:"max_networks" gorm:"not null;default:0"`
	MaxAPIKeys           int `json:"max_api_keys" gorm:"not null;default:0"`
	// MaxMembers caps the workspace's member count (owner + invited members).
	MaxMembers int `json:"max_members" gorm:"not null;default:0"`
	// MaxDatabasesPerInstance caps logical databases within a single instance.
	MaxDatabasesPerInstance int `json:"max_databases_per_instance" gorm:"not null;default:0"`
	// MaxCPUCores / MaxMemoryMB cap the workspace's aggregate app compute.
	MaxCPUCores int `json:"max_cpu_cores" gorm:"not null;default:0"`
	MaxMemoryMB int `json:"max_memory_mb" gorm:"not null;default:0"`
	// MaxDatabaseInstanceSizeMB caps a single DB instance's declared data-volume
	// size; MaxStorageMB caps the workspace's aggregate declared storage
	// (volumes + DB instance data volumes).
	MaxDatabaseInstanceSizeMB int `json:"max_database_instance_size_mb" gorm:"not null;default:0"`
	MaxStorageMB              int `json:"max_storage_mb" gorm:"not null;default:0"`
	// MaxRunners caps how many build/pipeline runners a workspace may register
	// (its own build machines). -1 = unlimited, 0 = none.
	MaxRunners int `json:"max_runners" gorm:"not null;default:0"`
	// MaxGPUs caps the aggregate number of whole GPU units a workspace's *running*
	// apps may hold at once (summed across apps, like MaxCPUCores). 0 = none,
	// -1 = unlimited. A stopped app frees its units.
	MaxGPUs int `json:"max_gpus" gorm:"not null;default:0"`

	// Capabilities (feature gates). Default false for the same omission reason.
	AllowCustomTLS            bool `json:"allow_custom_tls" gorm:"not null;default:false"`
	AllowPrivilegedHostMounts bool `json:"allow_privileged_host_mounts" gorm:"not null;default:false"`
	// AllowShellExec gates opening an interactive shell (docker exec) into a
	// running application container from the panel.
	AllowShellExec bool `json:"allow_shell_exec" gorm:"not null;default:false"`
	// AllowSharedStorage gates creating shared-storage volumes (NFS / CIFS-SMB)
	// that replicas can mount read-write across nodes. Node-local volumes are
	// always allowed; this only governs the rwx backends.
	AllowSharedStorage bool `json:"allow_shared_storage" gorm:"not null;default:false"`
	// AllowDNSProviders gates connecting a managed DNS provider (Cloudflare/Route
	// 53/DigitalOcean) for automated ownership verification + app records. Manual
	// (copy-paste) DNS always works; this only governs the automation.
	AllowDNSProviders bool `json:"allow_dns_providers" gorm:"not null;default:false"`
	// AllowCustomLabels gates attaching user-defined Docker labels to app
	// containers (for label-driven tools like Traefik). Off by default; the
	// reserved-prefix protection (io.miabi.*, com.docker.*) applies regardless.
	AllowCustomLabels bool `json:"allow_custom_labels" gorm:"not null;default:false"`
	// AllowPlatformRunners grants this workspace's build/pipeline jobs access to
	// the platform-shared runner pool (in addition to any runners it owns). Off by
	// default; owned runners are always usable.
	AllowPlatformRunners bool `json:"allow_platform_runners" gorm:"not null;default:false"`
	// AllowCustomBuilder gates a per-app custom buildpack builder image. A custom
	// builder runs on the runner with docker-daemon access, so it is a privileged
	// input on shared/multi-tenant runners; off by default (the platform default
	// builder is used instead).
	AllowCustomBuilder bool `json:"allow_custom_builder" gorm:"not null;default:false"`
	// SecurityProfile hardens how this workspace's application and job containers
	// run. "" / "default" = the image's default user (unchanged); "restricted"
	// forces a non-root platform UID. Default "default" is the zero-equivalent, so
	// the GORM zero-value omission still round-trips an intentional value.
	SecurityProfile string `json:"security_profile" gorm:"not null;default:'default'"`
	// AllowOfficialImageUser lets applications installed from an official
	// marketplace template keep the image's own default user even when this
	// workspace's SecurityProfile is "restricted". Only official-source installs
	// qualify (a tenant cannot self-declare an app official), and it never relaxes
	// a platform-wide MIABI_FORCE_NON_ROOT_USER mandate. Off by default.
	AllowOfficialImageUser bool `json:"allow_official_image_user" gorm:"not null;default:false"`
	// AllowGPU gates whether this workspace's apps may request GPU devices at all.
	// Off by default, like AllowSharedStorage: a workspace cannot request any GPU
	// until its plan opts in. Attaching a GPU is device passthrough (privileged),
	// so it is gated hard here and is incompatible with the restricted profile.
	AllowGPU bool `json:"allow_gpu" gorm:"not null;default:false"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WorkspaceQuota holds per-workspace overrides applied on top of the assigned
// plan. Any non-nil field overrides the plan for that workspace; nil inherits.
type WorkspaceQuota struct {
	WorkspaceID               uint    `json:"workspace_id" gorm:"primaryKey"`
	MaxApps                   *int    `json:"max_apps,omitempty"`
	MaxDatabaseInstances      *int    `json:"max_database_instances,omitempty"`
	MaxCronJobs               *int    `json:"max_cron_jobs,omitempty"`
	MaxVolumes                *int    `json:"max_volumes,omitempty"`
	MaxNetworks               *int    `json:"max_networks,omitempty"`
	MaxAPIKeys                *int    `json:"max_api_keys,omitempty"`
	MaxMembers                *int    `json:"max_members,omitempty"`
	MaxDatabasesPerInstance   *int    `json:"max_databases_per_instance,omitempty"`
	MaxCPUCores               *int    `json:"max_cpu_cores,omitempty"`
	MaxMemoryMB               *int    `json:"max_memory_mb,omitempty"`
	MaxDatabaseInstanceSizeMB *int    `json:"max_database_instance_size_mb,omitempty"`
	MaxStorageMB              *int    `json:"max_storage_mb,omitempty"`
	MaxRunners                *int    `json:"max_runners,omitempty"`
	MaxGPUs                   *int    `json:"max_gpus,omitempty"`
	AllowCustomTLS            *bool   `json:"allow_custom_tls,omitempty"`
	AllowPrivilegedHostMounts *bool   `json:"allow_privileged_host_mounts,omitempty"`
	AllowShellExec            *bool   `json:"allow_shell_exec,omitempty"`
	AllowSharedStorage        *bool   `json:"allow_shared_storage,omitempty"`
	AllowDNSProviders         *bool   `json:"allow_dns_providers,omitempty"`
	AllowCustomLabels         *bool   `json:"allow_custom_labels,omitempty"`
	AllowPlatformRunners      *bool   `json:"allow_platform_runners,omitempty"`
	AllowCustomBuilder        *bool   `json:"allow_custom_builder,omitempty"`
	AllowGPU                  *bool   `json:"allow_gpu,omitempty"`
	SecurityProfile           *string `json:"security_profile,omitempty"`          // nil = inherit plan
	AllowOfficialImageUser    *bool   `json:"allow_official_image_user,omitempty"` // nil = inherit plan

	UpdatedAt time.Time `json:"updated_at"`
}
