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
	ID                        uint      `json:"id" gorm:"primaryKey"`
	Name                      string    `json:"name" gorm:"uniqueIndex;not null"`
	Description               string    `json:"description"`
	IsDefault                 bool      `json:"is_default" gorm:"not null;default:false"` // applied to workspaces with no plan
	IsActive                  bool      `json:"is_active" gorm:"not null;default:true"`
	System                    bool      `json:"system" gorm:"not null;default:false"`
	MaxApps                   int       `json:"max_apps" gorm:"not null;default:0"`
	MaxDatabaseInstances      int       `json:"max_database_instances" gorm:"not null;default:0"`
	MaxCronJobs               int       `json:"max_cron_jobs" gorm:"not null;default:0"`
	MaxVolumes                int       `json:"max_volumes" gorm:"not null;default:0"`
	MaxNetworks               int       `json:"max_networks" gorm:"not null;default:0"`
	MaxAPIKeys                int       `json:"max_api_keys" gorm:"not null;default:0"`
	MaxMembers                int       `json:"max_members" gorm:"not null;default:0"`
	MaxDatabasesPerInstance   int       `json:"max_databases_per_instance" gorm:"not null;default:0"`
	MaxCPUCores               int       `json:"max_cpu_cores" gorm:"not null;default:0"`
	MaxMemoryMB               int       `json:"max_memory_mb" gorm:"not null;default:0"`
	MaxDatabaseInstanceSizeMB int       `json:"max_database_instance_size_mb" gorm:"not null;default:0"`
	MaxStorageMB              int       `json:"max_storage_mb" gorm:"not null;default:0"`
	MaxRunners                int       `json:"max_runners" gorm:"not null;default:0"`
	MaxGPUs                   int       `json:"max_gpus" gorm:"not null;default:0"`
	AllowCustomTLS            bool      `json:"allow_custom_tls" gorm:"not null;default:false"`
	AllowPrivilegedHostMounts bool      `json:"allow_privileged_host_mounts" gorm:"not null;default:false"`
	AllowShellExec            bool      `json:"allow_shell_exec" gorm:"not null;default:false"`
	AllowSharedStorage        bool      `json:"allow_shared_storage" gorm:"not null;default:false"`
	AllowDNSProviders         bool      `json:"allow_dns_providers" gorm:"not null;default:false"`
	AllowCustomLabels         bool      `json:"allow_custom_labels" gorm:"not null;default:false"`
	AllowPlatformRunners      bool      `json:"allow_platform_runners" gorm:"not null;default:false"`
	AllowCustomBuilder        bool      `json:"allow_custom_builder" gorm:"not null;default:false"`
	SecurityProfile           string    `json:"security_profile" gorm:"not null;default:'default'"`
	AllowOfficialImageUser    bool      `json:"allow_official_image_user" gorm:"not null;default:false"`
	AllowGPU                  bool      `json:"allow_gpu" gorm:"not null;default:false"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
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
