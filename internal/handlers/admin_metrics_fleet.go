// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/nodestats"
)

// FleetCapacity is what the fleet physically has, and how much of it workloads have already claimed.
// Committed is a reservation total, not utilization: it is what decides whether the next app can be
// scheduled, and it is the only one of the two the control plane can know exactly.
type FleetCapacity struct {
	CPUCores    int   `json:"cpu_cores"`
	MemoryBytes int64 `json:"memory_bytes"`
	// NodesCounted is how many nodes answered. Capacity is the sum over those, so a node that was
	// unreachable at the last refresh is absent from the totals rather than counted as zero.
	NodesCounted int `json:"nodes_counted"`
	NodesTotal   int `json:"nodes_total"`
	// Committed sums the explicit CPU and memory limits set on applications and database instances.
	// Workloads with no limit are invisible here, so this is a floor, not a ceiling.
	CommittedNanoCPUs    int64 `json:"committed_nano_cpus"`
	CommittedMemoryBytes int64 `json:"committed_memory_bytes"`
	// MeasuredAt is when the node capacities were last collected (they are cached; see fleetTTL).
	MeasuredAt time.Time `json:"measured_at"`

	// Real utilization, sampled on the nodes themselves. CPUPercent is weighted by each node's core
	// count, so a busy 16-core node counts for more than a busy 2-core one. NodesSampled is how many
	// answered; zero means no figure, not an idle fleet.
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryUsedBytes int64   `json:"memory_used_bytes"`
	NodesSampled    int     `json:"nodes_sampled"`
}

// StorageClassStats aggregates the operator's registered disks. Capacity and free space come from
// the storage-class sweep's cached columns, so this costs one query and never touches a node.
type StorageClassStats struct {
	Total   int64 `json:"total"`   // registered classes, including the built-in one
	Managed int64 `json:"managed"` // classes that own a path, i.e. have a disk to measure
	// CapacityBytes and AvailableBytes sum only classes that share no filesystem, so two classes on
	// one disk would double-count; the per-class figures below are the reliable ones.
	CapacityBytes  int64 `json:"capacity_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
	// Fullest names the class closest to full, which is the one that will break something first.
	FullestName string `json:"fullest_name,omitempty"`
	FullestPct  int    `json:"fullest_pct,omitempty"`
	// Unmeasured counts managed classes the sweep has never sized — usually a node it cannot reach.
	Unmeasured int64 `json:"unmeasured"`
}

// PlatformSignals are the things an operator wants to be told about without going looking: expiring
// certificates, firing alerts, and whether the platform's own backup is still running.
type PlatformSignals struct {
	CertsExpiringSoon int64      `json:"certs_expiring_soon"` // valid, but inside certExpiryWindow
	CertsExpired      int64      `json:"certs_expired"`
	FiringAlerts      int64      `json:"firing_alerts"`
	LastBackupAt      *time.Time `json:"last_backup_at,omitempty"`
	LastBackupFailed  bool       `json:"last_backup_failed"`
}

const (
	// fleetTTL bounds how often node capacity is collected: it is one Docker Info per node, and the
	// dashboard streams every 5s, so it must not ride the tick.
	fleetTTL = 60 * time.Second
	// fleetProbeTimeout keeps one unreachable node from stalling the whole snapshot.
	fleetProbeTimeout = 3 * time.Second
	// fleetConcurrency bounds how many nodes are probed at once, so the sweep is flat in fleet size
	// without opening a connection per node on a large one.
	fleetConcurrency = 8
	// fleetRefreshTimeout caps a whole background sweep, so a wedged node cannot leave the refresh
	// flag set forever and freeze the figures.
	fleetRefreshTimeout = 2 * time.Minute
	// certExpiryWindow is how far ahead a certificate counts as "expiring soon". Two weeks is long
	// enough to act on a renewal that is not self-healing, short enough not to cry wolf.
	certExpiryWindow = 14 * 24 * time.Hour
)

// NodeCapacityClients resolves per-node Docker clients for the capacity sweep. Satisfied by
// nodes.Clients; nil leaves the fleet panel out rather than reporting zeros.
type NodeCapacityClients interface {
	For(serverID uint) (docker.Client, error)
}

// NodeHostStats reads a node's real CPU/memory use. Satisfied by nodestats.Service; nil leaves the
// fleet figures as capacity and commitment only.
type NodeHostStats interface {
	Get(ctx context.Context, serverID uint) (nodestats.Sample, error)
}

// SetNodeHostStats wires the per-node host sampler (nil-safe).
func (h *AdminMetricsHandler) SetNodeHostStats(s NodeHostStats) { h.nodeStats = s }

// SetNodeClients wires the per-node Docker clients used for fleet capacity (nil-safe).
func (h *AdminMetricsHandler) SetNodeClients(c NodeCapacityClients) { h.nodeClients = c }

// fleetCache holds the last capacity sweep, so the SSE tick reads memory instead of the fleet.
type fleetCache struct {
	mu         sync.Mutex
	at         time.Time
	data       FleetCapacity
	refreshing bool
}

// fleet returns fleet capacity WITHOUT ever probing on the caller's path. A probe costs a Docker
// round trip and a one-second sample per node, so doing it inline made the dashboard slower the
// more nodes an install had — and, while it held the cache lock, blocked every other reader too.
// A stale answer is served immediately and the refresh happens behind it; the dashboard streams
// every 5s, so the new figures arrive on their own.
//
// The committed half is a pair of SQL sums, cheap enough to recompute per call and so always live.
func (h *AdminMetricsHandler) fleet(ctx context.Context) *FleetCapacity {
	if h.nodeClients == nil {
		return nil
	}
	h.fleetCache.mu.Lock()
	out, at, refreshing := h.fleetCache.data, h.fleetCache.at, h.fleetCache.refreshing
	if (at.IsZero() || time.Since(at) > fleetTTL) && !refreshing {
		h.fleetCache.refreshing = true
		go h.refreshFleet()
	}
	h.fleetCache.mu.Unlock()

	out.CommittedNanoCPUs = h.sumBytes(&models.Application{}, "nano_cpus") +
		h.sumBytes(&models.DatabaseInstance{}, "nano_cpus")
	out.CommittedMemoryBytes = h.sumBytes(&models.Application{}, "memory_bytes") +
		h.sumBytes(&models.DatabaseInstance{}, "memory_bytes")
	return &out
}

// refreshFleet probes the fleet in the background. It deliberately does NOT use the request context:
// the request that triggered it has usually returned long before the probe finishes.
func (h *AdminMetricsHandler) refreshFleet() {
	ctx, cancel := context.WithTimeout(context.Background(), fleetRefreshTimeout)
	defer cancel()
	data := h.probeFleet(ctx)

	h.fleetCache.mu.Lock()
	h.fleetCache.data = data
	h.fleetCache.at = time.Now()
	h.fleetCache.refreshing = false
	h.fleetCache.mu.Unlock()
}

// probeFleet asks every node that should be reachable for its CPU and memory totals.
func (h *AdminMetricsHandler) probeFleet(ctx context.Context) FleetCapacity {
	var servers []models.Server
	if err := h.db.Find(&servers).Error; err != nil {
		return FleetCapacity{}
	}
	out := FleetCapacity{NodesTotal: len(servers), MeasuredAt: time.Now()}
	var weighted, weight float64

	// Nodes are probed concurrently: sequentially, one unreachable node spent its whole timeout
	// before the next was even tried, so the sweep grew with the size of the fleet. Bounded, so a
	// large fleet does not open an unbounded number of Docker connections at once.
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, fleetConcurrency)
	for i := range servers {
		s := servers[i]
		if !s.IsLocal && s.Status == models.ServerStatusOffline {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			dc, err := h.nodeClients.For(s.ID)
			if err != nil {
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, fleetProbeTimeout)
			info, err := dc.Info(probeCtx)
			cancel()
			if err != nil {
				return
			}
			mu.Lock()
			out.CPUCores += info.CPUs
			out.MemoryBytes += info.MemTotal
			out.NodesCounted++
			mu.Unlock()

			// Sampling runs a container on the node, so it rides this TTL sweep rather than the 5s
			// stream tick. A node that cannot be sampled simply contributes no utilization.
			if h.nodeStats == nil {
				return
			}
			st, serr := h.nodeStats.Get(ctx, s.ID)
			// A sample that describes the physical machine rather than this node is left out of the
			// totals: several nodes on one box would each add that box's whole usage, which is how
			// the fleet came to report more memory in use than it has.
			if serr != nil || !st.DescribesNode {
				return
			}
			mu.Lock()
			used := int64(st.MemUsedBytes)
			if used > info.MemTotal {
				used = info.MemTotal
			}
			weighted += st.CPUPercent * float64(info.CPUs)
			weight += float64(info.CPUs)
			out.MemoryUsedBytes += used
			out.NodesSampled++
			mu.Unlock()
		}()
	}
	wg.Wait()

	if weight > 0 {
		out.CPUPercent = weighted / weight
	}
	return out
}

// storageClasses aggregates the registered classes and finds the one closest to full.
func (h *AdminMetricsHandler) storageClasses() StorageClassStats {
	out := StorageClassStats{
		Total:   h.count(&models.StorageClass{}, ""),
		Managed: h.countArgs(&models.StorageClass{}, "path <> ?", ""),
	}
	if out.Managed == 0 {
		return out
	}
	var classes []models.StorageClass
	if err := h.db.Where("path <> ? AND enabled = ?", "", true).Find(&classes).Error; err != nil {
		return out
	}
	for i := range classes {
		c := classes[i]
		if c.CapacityBytes <= 0 {
			out.Unmeasured++
			continue
		}
		out.CapacityBytes += c.CapacityBytes
		out.AvailableBytes += c.AvailableBytes
		pct := int(float64(c.CapacityBytes-c.AvailableBytes) / float64(c.CapacityBytes) * 100)
		if pct > out.FullestPct {
			out.FullestPct, out.FullestName = pct, c.DisplayName
			if out.FullestName == "" {
				out.FullestName = c.Name
			}
		}
	}
	return out
}

// signals collects the "tell me before I find out" figures.
func (h *AdminMetricsHandler) signals() PlatformSignals {
	now := time.Now()
	out := PlatformSignals{
		CertsExpiringSoon: h.countArgs(&models.Certificate{}, "not_after > ? AND not_after <= ?", now, now.Add(certExpiryWindow)),
		CertsExpired:      h.countArgs(&models.Certificate{}, "not_after <= ?", now),
		FiringAlerts:      h.countArgs(&models.Alert{}, "state = ?", string(models.AlertFiring)),
	}
	var last models.PlatformBackup
	if err := h.db.Where("status = ?", string(models.BackupCompleted)).
		Order("created_at DESC").First(&last).Error; err == nil {
		at := last.CreatedAt
		if last.FinishedAt != nil {
			at = *last.FinishedAt
		}
		out.LastBackupAt = &at
	}
	var newest models.PlatformBackup
	if err := h.db.Order("created_at DESC").First(&newest).Error; err == nil {
		out.LastBackupFailed = newest.Status == models.BackupFailed
	}
	return out
}
