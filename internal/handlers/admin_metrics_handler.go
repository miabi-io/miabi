// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// SubnetPoolStater reports network subnet-pool utilization. Satisfied by
// *netalloc.Service; injected so the admin dashboard can show it.
type SubnetPoolStater interface {
	Stats() (used int, total int)
}

// AdminMetricsHandler aggregates platform-wide counts and runtime stats.
type AdminMetricsHandler struct {
	db        *gorm.DB
	docker    docker.Client
	inspector *asynq.Inspector // lists connected asynq worker servers; nil disables the panel
	startTime time.Time
	subnets   SubnetPoolStater // nil = no network-pool panel
}

// SetSubnetAllocator wires the subnet-pool stats source (nil-safe).
func (h *AdminMetricsHandler) SetSubnetAllocator(s SubnetPoolStater) { h.subnets = s }

func NewAdminMetricsHandler(db *gorm.DB, dockerClient docker.Client, redisClient *redis.Client, startTime time.Time) *AdminMetricsHandler {
	var insp *asynq.Inspector
	if redisClient != nil {
		insp = asynq.NewInspectorFromRedisClient(redisClient)
	}
	return &AdminMetricsHandler{db: db, docker: dockerClient, inspector: insp, startTime: startTime}
}

// PlatformMetrics is the snapshot returned to the admin dashboard.
type PlatformMetrics struct {
	TotalUsers        int64 `json:"total_users"`
	ActiveUsers       int64 `json:"active_users"`
	AdminUsers        int64 `json:"admin_users"`
	TotalWorkspaces   int64 `json:"total_workspaces"`
	TotalApplications int64 `json:"total_applications"`
	TotalDatabases    int64 `json:"total_databases"`
	TotalStacks       int64 `json:"total_stacks"`
	TotalVolumes      int64 `json:"total_volumes"`
	TotalNodes        int64 `json:"total_nodes"`
	TotalRoutes       int64 `json:"total_routes"`
	ActiveSessions    int64 `json:"active_sessions"`

	RunningContainers int `json:"running_containers"`
	TotalContainers   int `json:"total_containers"`

	ConnectedWorkers int          `json:"connected_workers"`
	Workers          []WorkerInfo `json:"workers"`

	// Build runners, split by scope (platform-shared vs workspace-owned) with the
	// online subset of each.
	SharedRunners          int64 `json:"shared_runners"`
	SharedRunnersOnline    int64 `json:"shared_runners_online"`
	WorkspaceRunners       int64 `json:"workspace_runners"`
	WorkspaceRunnersOnline int64 `json:"workspace_runners_online"`

	// Platform-wide volume storage: declared capacity vs last-measured on-disk use.
	StorageDeclaredBytes int64 `json:"storage_declared_bytes"`
	StorageUsedBytes     int64 `json:"storage_used_bytes"`

	UptimeSeconds   float64 `json:"uptime_seconds"`
	Goroutines      int     `json:"goroutines"`
	MemoryAllocByte uint64  `json:"memory_alloc_bytes"`

	Version string `json:"version"`
	Commit  string `json:"commit"`

	// NetworkPool reports managed-subnet-pool utilization; nil when the allocator
	// is disabled.
	NetworkPool *NetworkPoolStats `json:"network_pool,omitempty"`
}

// NetworkPoolStats is the managed network subnet pool's utilization.
type NetworkPoolStats struct {
	Used      int `json:"used"`      // subnets allocated or reserved
	Available int `json:"available"` // subnets still free
	Total     int `json:"total"`     // pool capacity
}

// WorkerInfo describes one connected asynq worker server (from the inspector).
type WorkerInfo struct {
	Host        string         `json:"host"`
	PID         int            `json:"pid"`
	Type        string         `json:"type"` // "embedded" (this process) or "standalone"
	Concurrency int            `json:"concurrency"`
	Queues      map[string]int `json:"queues"`
	ActiveTasks int            `json:"active_tasks"`
	Status      string         `json:"status"` // asynq server status: "active" / "stopped" / "quiet"
	Started     time.Time      `json:"started"`
}

// Snapshot returns a one-shot metrics payload.
func (h *AdminMetricsHandler) Snapshot(c *okapi.Context) error {
	return ok(c, h.collect(c.Request().Context()))
}

// Stream pushes a fresh metrics snapshot over SSE every few seconds.
func (h *AdminMetricsHandler) Stream(c *okapi.Context) error {
	ctx := c.Request().Context()
	if err := c.SSESendJSON(h.collect(ctx)); err != nil {
		return nil
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.SSESendJSON(h.collect(ctx)); err != nil {
				return nil
			}
		}
	}
}

func (h *AdminMetricsHandler) collect(ctx context.Context) PlatformMetrics {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	pm := PlatformMetrics{
		TotalUsers:        h.count(&models.User{}, ""),
		ActiveUsers:       h.count(&models.User{}, "active = true"),
		AdminUsers:        h.countArgs(&models.User{}, "role = ?", string(models.SystemRoleAdmin)),
		TotalWorkspaces:   h.count(&models.Workspace{}, ""),
		TotalApplications: h.count(&models.Application{}, ""),
		TotalDatabases:    h.count(&models.DatabaseInstance{}, ""),
		TotalStacks:       h.count(&models.Stack{}, ""),
		TotalVolumes:      h.count(&models.Volume{}, ""),
		TotalNodes:        h.count(&models.Server{}, ""),
		TotalRoutes:       h.count(&models.Route{}, ""),
		ActiveSessions:    h.countArgs(&models.Session{}, "revoked = false AND expires_at > ?", time.Now()),
		UptimeSeconds:     time.Since(h.startTime).Seconds(),
		Goroutines:        runtime.NumGoroutine(),
		MemoryAllocByte:   m.Alloc,
		Version:           config.Version,
		Commit:            config.CommitID,

		SharedRunners:          h.countArgs(&models.Runner{}, "scope = ?", string(models.ScopeShared)),
		SharedRunnersOnline:    h.countArgs(&models.Runner{}, "scope = ? AND status = ?", string(models.ScopeShared), string(models.RunnerStatusOnline)),
		WorkspaceRunners:       h.countArgs(&models.Runner{}, "scope = ?", string(models.ScopeWorkspace)),
		WorkspaceRunnersOnline: h.countArgs(&models.Runner{}, "scope = ? AND status = ?", string(models.ScopeWorkspace), string(models.RunnerStatusOnline)),

		StorageDeclaredBytes: h.sumBytes(&models.Volume{}, "size_bytes"),
		StorageUsedBytes:     h.sumBytes(&models.Volume{}, "used_bytes"),
	}

	if h.docker != nil {
		if all, err := h.docker.ListContainers(ctx, true); err == nil {
			pm.TotalContainers = len(all)
			for _, ct := range all {
				if ct.State == "running" {
					pm.RunningContainers++
				}
			}
		}
	}

	pm.Workers = h.collectWorkers()
	pm.ConnectedWorkers = len(pm.Workers)

	if h.subnets != nil {
		used, total := h.subnets.Stats()
		pm.NetworkPool = &NetworkPoolStats{Used: used, Available: total - used, Total: total}
	}
	return pm
}

// collectWorkers lists the connected asynq worker servers via the inspector. The control-plane
// server runs an embedded worker and standalone workers share the same Redis. Returns an empty
// slice when the inspector is unavailable.
func (h *AdminMetricsHandler) collectWorkers() []WorkerInfo {
	if h.inspector == nil {
		return nil
	}
	servers, err := h.inspector.Servers()
	if err != nil {
		return nil
	}
	selfPID := os.Getpid()
	selfHost, _ := os.Hostname()

	workers := make([]WorkerInfo, 0, len(servers))
	for _, s := range servers {
		wType := "standalone"
		if s.PID == selfPID && s.Host == selfHost {
			wType = "embedded"
		}
		workers = append(workers, WorkerInfo{
			Host:        s.Host,
			PID:         s.PID,
			Type:        wType,
			Concurrency: s.Concurrency,
			Queues:      s.Queues,
			ActiveTasks: len(s.ActiveWorkers),
			Status:      s.Status,
			Started:     s.Started,
		})
	}
	// Stable order so the live SSE table doesn't reshuffle between ticks.
	sort.Slice(workers, func(i, j int) bool {
		if workers[i].Host != workers[j].Host {
			return workers[i].Host < workers[j].Host
		}
		return workers[i].PID < workers[j].PID
	})
	return workers
}

func (h *AdminMetricsHandler) count(model any, where string) int64 {
	var n int64
	q := h.db.Model(model)
	if where != "" {
		q = q.Where(where)
	}
	q.Count(&n)
	return n
}

func (h *AdminMetricsHandler) countArgs(model any, where string, args ...any) int64 {
	var n int64
	h.db.Model(model).Where(where, args...).Count(&n)
	return n
}

func (h *AdminMetricsHandler) sumBytes(model any, col string) int64 {
	var n int64
	h.db.Model(model).Select("COALESCE(SUM(" + col + "),0)").Scan(&n)
	return n
}
