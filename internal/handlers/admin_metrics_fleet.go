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
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// FleetCapacity Committed is a reservation total, not utilization; it decides whether the next app fits.
type FleetCapacity struct {
	CPUCores    int   `json:"cpu_cores"`
	MemoryBytes int64 `json:"memory_bytes"`
	// Unreachable nodes are absent from the totals rather than counted as zero.
	NodesCounted int `json:"nodes_counted"`
	NodesTotal   int `json:"nodes_total"`
	// Workloads with no limit are invisible here, so this is a floor, not a ceiling.
	CommittedNanoCPUs    int64 `json:"committed_nano_cpus"`
	CommittedMemoryBytes int64 `json:"committed_memory_bytes"`
	// StorageBytes and StorageFreeBytes size the filesystems behind each node's Docker data root.
	StorageBytes     int64 `json:"storage_bytes"`
	StorageFreeBytes int64 `json:"storage_free_bytes"`

	// CPUPercent is weighted by core count; NodesSampled zero means no figure, not an idle fleet.
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryUsedBytes int64   `json:"memory_used_bytes"`
	NodesSampled    int     `json:"nodes_sampled"`
}

// StorageClassStats reads the sweep's cached columns, so it never touches a node.
type StorageClassStats struct {
	Total   int64 `json:"total"`   // registered classes, including the built-in one
	Managed int64 `json:"managed"` // classes that own a path, i.e. have a disk to measure
	// Summed only over classes sharing no filesystem to avoid double-counting; per-class figures are reliable.
	CapacityBytes  int64 `json:"capacity_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
	// Fullest names the class closest to full, which is the one that will break something first.
	FullestName string `json:"fullest_name,omitempty"`
	FullestPct  int    `json:"fullest_pct,omitempty"`
	// Unmeasured counts managed classes the sweep has never sized — usually a node it cannot reach.
	Unmeasured int64 `json:"unmeasured"`
}

type PlatformSignals struct {
	CertsExpiringSoon int64      `json:"certs_expiring_soon"` // valid, but inside certExpiryWindow
	CertsExpired      int64      `json:"certs_expired"`
	FiringAlerts      int64      `json:"firing_alerts"`
	LastBackupAt      *time.Time `json:"last_backup_at,omitempty"`
	LastBackupFailed  bool       `json:"last_backup_failed"`
}

const (
	// A node that stopped reporting fades out of fleet figures instead of freezing them.
	usageMaxAge = 5 * time.Minute
	// Two weeks is long enough to act on a non-self-healing renewal, short enough not to cry wolf.
	certExpiryWindow = 14 * 24 * time.Hour
)

// NodeCapacityStore reads the stored fleet capacity. Satisfied by repositories.ServerRepository.
type NodeCapacityStore interface {
	SumCapacity(clusterID uint, usageMaxAge time.Duration) (repositories.Capacity, error)
}

// SetNodeCapacity wires the stored-capacity reader (nil-safe).
func (h *AdminMetricsHandler) SetNodeCapacity(c NodeCapacityStore) { h.nodeCapacity = c }

// Reads stored capacity so the dashboard stays flat in node count; probing here blocked concurrent readers.
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
