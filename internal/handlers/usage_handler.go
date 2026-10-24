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
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ClusterCap reports whether any cluster runs a swarm — surfaced as a workspace capability so any
// member, not just a platform admin, can tell whether the "service" runtime is offerable when
// creating an app. Injected after construction (nil = cluster mode off).
type ClusterCap interface {
	AnySwarm() bool
	IsSwarm(clusterID uint) bool
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
	placer     *Placer
}

func NewUsageHandler(q *quota.Service, apps *repositories.ApplicationRepository, dbs *repositories.DatabaseRepository, volumes *repositories.VolumeRepository, networks *repositories.NetworkRepository, jobs *repositories.JobRepository, apiKeys *repositories.APIKeyRepository, workspaces *repositories.WorkspaceRepository, runners *repositories.RunnerRepository) *UsageHandler {
	return &UsageHandler{quota: q, apps: apps, dbs: dbs, volumes: volumes, networks: networks, jobs: jobs, apiKeys: apiKeys, workspaces: workspaces, runners: runners}
}

// SetClusterCap wires the cluster-capability check surfaced to workspace members.
func (h *UsageHandler) SetClusterCap(c ClusterCap) { h.cluster = c }

// SetPlacer narrows cluster_enabled to the locations the workspace may use (nil-safe; nil reports every cluster).
func (h *UsageHandler) SetPlacer(p *Placer) { h.placer = p }

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
	PlanName string        `json:"plan_name"`
	Limits   quota.Limits  `json:"limits"`
	Apps     ResourceUsage `json:"apps"`
	// AppsWithoutLimits counts apps missing a CPU or memory limit: they run unbounded and the CPU and
	// memory totals don't see them.
	AppsWithoutLimits int64         `json:"apps_without_limits"`
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
		// ServiceRuntime reports whether this workspace may run an app as a replicated service: the
		// cluster is on and the plan allows it.
		ServiceRuntime bool `json:"service_runtime"`
	} `json:"capabilities"`

	// The database budget, summed over instance limits and apart from the apps' CPU and memory.
	DatabaseCPUCores ResourceUsage `json:"database_cpu_cores"`
	DatabaseMemoryMB ResourceUsage `json:"database_memory_mb"`
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
	unlimitedN, _ := h.apps.CountWithoutLimitsByWorkspace(wsID)
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
	dbCPUNano, dbMemBytes, _ := h.dbs.SumResourcesByWorkspace(wsID, 0)

	u := WorkspaceUsage{
		Enforced:          h.quota.Enabled(),
		PlanName:          h.quota.EffectivePlanName(wsID),
		Limits:            l,
		Apps:              ResourceUsage{Used: appN, Limit: l.MaxApps},
		AppsWithoutLimits: unlimitedN,
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
		DatabaseCPUCores:  ResourceUsage{Used: dbCPUNano / nanoPerCore, Limit: l.MaxDatabaseCPUCores},
		DatabaseMemoryMB:  ResourceUsage{Used: dbMemBytes / bytesPerMB, Limit: l.MaxDatabaseMemoryMB},
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
	u.Capabilities.ClusterEnabled = h.swarmAvailable(c, wsID)
	u.Capabilities.ServiceRuntime = u.Capabilities.ClusterEnabled && (!u.Enforced || l.AllowServiceRuntime)
	return ok(c, u)
}

// Limits reports a workspace's effective limits, overrides included.
func (h *UsageHandler) Limits(wsID uint) any { return h.quota.EffectiveLimits(wsID) }

// Measure reports a workspace's usage under the Billing API's Usage keys.
func (h *UsageHandler) Measure(wsID uint) map[string]float64 {
	appN, _ := h.apps.CountByWorkspace(wsID)
	dbN, _ := h.dbs.CountInstancesByWorkspace(wsID)
	cronN, _ := h.jobs.CountCronByWorkspace(wsID)
	volN, _ := h.volumes.CountByWorkspace(wsID)
	memberN, _ := h.workspaces.CountMembers(wsID)
	cpuNano, memBytes, _ := h.apps.SumResourcesByWorkspace(wsID, 0)
	volBytes, _ := h.volumes.SumSizeByWorkspace(wsID)
	dbBytes, _ := h.dbs.SumVolumeSizeByWorkspace(wsID)
	return map[string]float64{
		"apps": float64(appN), "database_instances": float64(dbN), "cron_jobs": float64(cronN), "volumes": float64(volN),
		"members": float64(memberN), "cpu_cores": float64(cpuNano) / nanoPerCore,
		"memory_mb": float64(memBytes / bytesPerMB), "storage_mb": float64((volBytes + dbBytes) / bytesPerMB),
	}
}

// swarmAvailable reports whether a location this workspace may use runs a swarm. A swarm elsewhere, such as
// another organization's dedicated cluster, is none of its business and offers it nothing.
func (h *UsageHandler) swarmAvailable(c *okapi.Context, wsID uint) bool {
	if h.cluster == nil {
		return false
	}
	if h.placer == nil || h.placer.svc == nil {
		return h.cluster.AnySwarm()
	}
	locs, err := h.placer.svc.Locations(wsID, h.placer.admin(c))
	if err != nil {
		return false
	}
	for _, l := range locs {
		if h.cluster.IsSwarm(l.ID) {
			return true
		}
	}
	return false
}
