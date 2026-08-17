// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ClusterCap reports whether cluster (Docker Swarm) mode is enabled — surfaced as a workspace
// capability so any member, not just a platform admin, can tell whether the "service" runtime is
// offerable when creating an app. Injected after construction (nil = cluster mode off).
type ClusterCap interface {
	CapCluster() bool
}

// UsageHandler reports a workspace's current resource usage against its plan.
type UsageHandler struct {
	quota      *quota.Service
	apps       *repositories.ApplicationRepository
	dbs        *repositories.DatabaseRepository
	volumes    *repositories.VolumeRepository
	networks   *repositories.NetworkRepository
	jobs       *repositories.JobRepository
	apiKeys    *repositories.APIKeyRepository
	workspaces *repositories.WorkspaceRepository
	runners    *repositories.RunnerRepository
	cluster    ClusterCap
}

func NewUsageHandler(q *quota.Service, apps *repositories.ApplicationRepository, dbs *repositories.DatabaseRepository, volumes *repositories.VolumeRepository, networks *repositories.NetworkRepository, jobs *repositories.JobRepository, apiKeys *repositories.APIKeyRepository, workspaces *repositories.WorkspaceRepository, runners *repositories.RunnerRepository) *UsageHandler {
	return &UsageHandler{quota: q, apps: apps, dbs: dbs, volumes: volumes, networks: networks, jobs: jobs, apiKeys: apiKeys, workspaces: workspaces, runners: runners}
}

// SetClusterCap wires the cluster-capability check surfaced to workspace members.
func (h *UsageHandler) SetClusterCap(c ClusterCap) { h.cluster = c }

// ResourceUsage pairs the live count with the effective limit (-1 = unlimited).
type ResourceUsage struct {
	Used  int64 `json:"used"`
	Limit int   `json:"limit"`
}

// WorkspaceUsage is the full usage-vs-limits snapshot for a workspace.
type WorkspaceUsage struct {
	// Enforced reports whether limits are actually applied (the platform flag).
	Enforced bool `json:"enforced"`
	// PlanName is the effective plan's name ("" = no plan / unlimited).
	PlanName          string        `json:"plan_name"`
	Limits            quota.Limits  `json:"limits"`
	Apps              ResourceUsage `json:"apps"`
	DatabaseInstances ResourceUsage `json:"database_instances"`
	CronJobs          ResourceUsage `json:"cron_jobs"`
	Volumes           ResourceUsage `json:"volumes"`
	Networks          ResourceUsage `json:"networks"`
	APIKeys           ResourceUsage `json:"api_keys"`
	Members           ResourceUsage `json:"members"`
	Runners           ResourceUsage `json:"runners"`
	// Aggregate compute & storage (used is the live sum; limit -1 = unlimited).
	CPUCores     ResourceUsage `json:"cpu_cores"` // used = whole cores rounded down
	MemoryMB     ResourceUsage `json:"memory_mb"`
	StorageMB    ResourceUsage `json:"storage_mb"`
	Capabilities struct {
		CustomTLS            bool `json:"custom_tls"`
		PrivilegedHostMounts bool `json:"privileged_host_mounts"`
		ShellExec            bool `json:"shell_exec"`
		SharedStorage        bool `json:"shared_storage"`
		DNSProviders         bool `json:"dns_providers"`
		CustomLabels         bool `json:"custom_labels"`
		CustomBuilder        bool `json:"custom_builder"`
		OfficialImageUser    bool `json:"official_image_user"`
		// RequireNonRoot reports that this workspace's containers must drop root —
		// the restricted profile or the server-level mandate. The UI uses it to
		// constrain the per-app / per-job run-as user to a non-root numeric uid.
		RequireNonRoot bool `json:"require_non_root"`
		// ClusterEnabled reports whether cluster (Swarm) mode is on, so the UI can
		// offer the replicated "service" runtime when creating an app. Not a plan
		// capability — a platform-level flag exposed here for any workspace member.
		ClusterEnabled bool `json:"cluster_enabled"`
	} `json:"capabilities"`
}

const (
	bytesPerMB  = 1024 * 1024
	nanoPerCore = 1_000_000_000
)

// Get returns the workspace's usage against its effective limits.
func (h *UsageHandler) Get(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	l := h.quota.EffectiveLimits(wsID)

	appN, _ := h.apps.CountByWorkspace(wsID)
	dbN, _ := h.dbs.CountInstancesByWorkspace(wsID)
	cronN, _ := h.jobs.CountCronByWorkspace(wsID)
	volN, _ := h.volumes.CountByWorkspace(wsID)
	netN, _ := h.networks.CountByWorkspace(wsID)
	keyN, _ := h.apiKeys.CountByWorkspace(wsID)
	memberN, _ := h.workspaces.CountMembers(wsID)
	runnerN, _ := h.runners.CountByWorkspace(wsID)
	cpuNano, memBytes, _ := h.apps.SumResourcesByWorkspace(wsID, 0)
	volBytes, _ := h.volumes.SumSizeByWorkspace(wsID)
	dbBytes, _ := h.dbs.SumVolumeSizeByWorkspace(wsID)

	u := WorkspaceUsage{
		Enforced:          h.quota.Enabled(),
		PlanName:          h.quota.EffectivePlanName(wsID),
		Limits:            l,
		Apps:              ResourceUsage{Used: appN, Limit: l.MaxApps},
		DatabaseInstances: ResourceUsage{Used: dbN, Limit: l.MaxDatabaseInstances},
		CronJobs:          ResourceUsage{Used: cronN, Limit: l.MaxCronJobs},
		Volumes:           ResourceUsage{Used: volN, Limit: l.MaxVolumes},
		Networks:          ResourceUsage{Used: netN, Limit: l.MaxNetworks},
		APIKeys:           ResourceUsage{Used: keyN, Limit: l.MaxAPIKeys},
		Members:           ResourceUsage{Used: memberN, Limit: l.MaxMembers},
		Runners:           ResourceUsage{Used: runnerN, Limit: l.MaxRunners},
		CPUCores:          ResourceUsage{Used: cpuNano / nanoPerCore, Limit: l.MaxCPUCores},
		MemoryMB:          ResourceUsage{Used: memBytes / bytesPerMB, Limit: l.MaxMemoryMB},
		StorageMB:         ResourceUsage{Used: (volBytes + dbBytes) / bytesPerMB, Limit: l.MaxStorageMB},
	}
	u.Capabilities.CustomTLS = l.AllowCustomTLS
	u.Capabilities.PrivilegedHostMounts = l.AllowPrivilegedHostMounts
	u.Capabilities.ShellExec = l.AllowShellExec
	u.Capabilities.SharedStorage = l.AllowSharedStorage
	u.Capabilities.DNSProviders = l.AllowDNSProviders
	u.Capabilities.CustomLabels = l.AllowCustomLabels
	u.Capabilities.CustomBuilder = l.AllowCustomBuilder
	u.Capabilities.OfficialImageUser = l.AllowOfficialImageUser
	u.Capabilities.RequireNonRoot = h.quota.RequireNonRootUser(wsID, false)
	u.Capabilities.ClusterEnabled = h.cluster != nil && h.cluster.CapCluster()
	return ok(c, u)
}
