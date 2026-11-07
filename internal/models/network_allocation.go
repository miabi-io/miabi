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

const (
	NetAllocKindWorkspace = "workspace" // a workspace's default/user network
	NetAllocKindStack     = "stack"     // a per-stack network
	NetAllocKindGateway   = "gateway"   // the shared reverse-proxy network
	NetAllocKindOverlay   = "overlay"   // a per-workspace swarm overlay
	// A pre-existing unmanaged subnet overlapping the pool, so the allocator never hands it out.
	NetAllocKindReserved = "reserved"
)

// NetworkAllocation is the allocator's ledger of pool subnets; reserved rows use DockerName "reserved:<subnet>".
type NetworkAllocation struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// DockerName is the globally-unique Docker network name this subnet is pinned to.
	DockerName string `json:"docker_name" gorm:"uniqueIndex;not null"`
	// Subnet is the allocated CIDR (e.g. 10.42.3.0/24); Gateway is its first host.
	Subnet  string `json:"subnet" gorm:"uniqueIndex;not null"`
	Gateway string `json:"gateway" gorm:"not null"`
	// Bridges are node-local, so one subnet may recur across nodes; uniqueness is on DockerName/Subnet.
	NodeID uint `json:"node_id" gorm:"not null;default:0"`
	// Kind labels the allocation (see NetAllocKind*).
	Kind      string    `json:"kind" gorm:"not null;default:workspace"`
	CreatedAt time.Time `json:"created_at"`
}
