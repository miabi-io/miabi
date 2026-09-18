// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
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
	// StorageBytes and StorageFreeBytes size the filesystems behind each node's Docker data root.
	StorageBytes     int64 `json:"storage_bytes"`
	StorageFreeBytes int64 `json:"storage_free_bytes"`

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
	// usageMaxAge bounds how old a node's usage reading may be before it stops counting toward the
	// fleet figures, so a node that quietly stopped reporting fades out instead of freezing them.
	usageMaxAge = 5 * time.Minute
	// certExpiryWindow is how far ahead a certificate counts as "expiring soon". Two weeks is long
	// enough to act on a renewal that is not self-healing, short enough not to cry wolf.
	certExpiryWindow = 14 * 24 * time.Hour
)

// NodeCapacityStore reads the stored fleet capacity. Satisfied by repositories.ServerRepository.
type NodeCapacityStore interface {
	SumCapacity(clusterID uint, usageMaxAge time.Duration) (repositories.Capacity, error)
}

// SetNodeCapacity wires the stored-capacity reader (nil-safe).
func (h *AdminMetricsHandler) SetNodeCapacity(c NodeCapacityStore) { h.nodeCapacity = c }

// fleet reports the fleet from STORED capacity: the node sweep measures each node on its own
// schedule and writes what it found, so the dashboard is a query rather than a fan-out of Docker
// calls. That is what keeps it flat in the number of nodes — probing here made it slower with
// every node added, and blocked every concurrent reader while it ran.
func (h *AdminMetricsHandler) fleet() *FleetCapacity {
	if h.nodeCapacity == nil {
		return nil
	}
	cap, err := h.nodeCapacity.SumCapacity(0, usageMaxAge)
	if err != nil {
		return nil
	}
	out := FleetCapacity{
		CPUCores:         int(cap.CPUCores),
		MemoryBytes:      cap.MemoryBytes,
		StorageBytes:     cap.StorageBytes,
		StorageFreeBytes: cap.StorageFreeBytes,
		NodesCounted:     int(cap.NodesMeasured),
		NodesTotal:       int(cap.Nodes),
		CPUPercent:       cap.CPUPercent,
		MemoryUsedBytes:  cap.MemUsedBytes,
		NodesSampled:     int(cap.NodesWithUsage),
	}
	out.CommittedNanoCPUs = h.sumBytes(&models.Application{}, "nano_cpus") +
		h.sumBytes(&models.DatabaseInstance{}, "nano_cpus")
	out.CommittedMemoryBytes = h.sumBytes(&models.Application{}, "memory_bytes") +
		h.sumBytes(&models.DatabaseInstance{}, "memory_bytes")
	return &out
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
