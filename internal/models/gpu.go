// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// GPUVendor is the vendor of a physical GPU. Only "nvidia" is supported in v1;
// "amd" is reserved for a later ROCm phase.
type GPUVendor string

const (
	GPUVendorNvidia GPUVendor = "nvidia"
	GPUVendorAMD    GPUVendor = "amd" // reserved (ROCm), not yet supported
)

// GPUDevice is one physical GPU discovered on a node's Docker host. A node
// advertises the GPUs it has (via the inventory probe); the platform admin
// controls whether each is offered to workloads. Rows are owned by a Server and
// keyed for re-inventory by the stable GPU UUID reported by nvidia-smi — never
// the host-local index, which can change across reboots.
//
// Devices get their own table (not a JSON blob on Server) because each is
// individually addressable: the admin enables/disables and marks shared or
// dedicated per card, and a device is the unit an app is scheduled onto. Same
// reasoning that gave network subnets their own rows.
type GPUDevice struct {
	ID       uint `json:"id" gorm:"primaryKey"`
	ServerID uint `json:"server_id" gorm:"index;not null"` // the node it lives on

	// UUID is the stable identity reported by the node agent (nvidia-smi -q -x),
	// e.g. "GPU-3f2c...". It is the join key across re-inventories.
	UUID     string    `json:"uuid" gorm:"uniqueIndex;not null"`
	Index    int       `json:"index"` // host-local device index at last inventory (informational)
	Vendor   GPUVendor `json:"vendor" gorm:"not null;default:nvidia"`
	Model    string    `json:"model"`     // "NVIDIA A100-SXM4-40GB"
	MemoryMB int       `json:"memory_mb"` // total framebuffer memory

	// Admin policy. A device is not offered to any workload until Enabled — a
	// newly discovered card always arrives disabled (fail closed).
	Enabled bool `json:"enabled" gorm:"not null;default:false"`
	// Shared: many apps may request this device concurrently (inference, dev).
	// Dedicated (false): reserved for one app at a time (training). Fleet-wide
	// exclusivity enforcement for dedicated cards lands in a later phase.
	Shared bool `json:"shared" gorm:"not null;default:true"`

	// LastSeenAt is stamped on every inventory scan that observes the device. A
	// device that drops out of a scan is kept (not deleted) so history and an
	// app's GPUKind reference survive a transient probe failure or a reboot
	// mid-scan; staleness is derived from an old LastSeenAt.
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
