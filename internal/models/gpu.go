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

package models

import "time"

// "amd" is reserved for a later ROCm phase.
type GPUVendor string

const (
	GPUVendorNvidia GPUVendor = "nvidia"
	GPUVendorAMD    GPUVendor = "amd" // reserved (ROCm), not yet supported
)

// GPUDevice is keyed by the stable nvidia-smi UUID, never the host-local index, which changes across reboots.
type GPUDevice struct {
	ID       uint `json:"id" gorm:"primaryKey"`
	ServerID uint `json:"server_id" gorm:"index;not null"` // the node it lives on

	UUID     string    `json:"uuid" gorm:"uniqueIndex;not null"`
	Index    int       `json:"index"` // host-local device index at last inventory (informational)
	Vendor   GPUVendor `json:"vendor" gorm:"not null;default:nvidia"`
	Model    string    `json:"model"`     // "NVIDIA A100-SXM4-40GB"
	MemoryMB int       `json:"memory_mb"` // total framebuffer memory

	// Newly discovered cards arrive disabled (fail closed).
	Enabled bool `json:"enabled" gorm:"not null;default:false"`
	// TODO: enforce fleet-wide exclusivity for dedicated (non-shared) cards.
	Shared bool `json:"shared" gorm:"not null;default:true"`

	// Missing devices are kept so history and GPUKind references survive a transient probe failure.
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
