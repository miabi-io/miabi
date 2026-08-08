// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Network-allocation kinds, recorded for observability and to distinguish real
// managed networks from subnets merely reserved to avoid an overlap.
const (
	NetAllocKindWorkspace = "workspace" // a workspace's default/user network
	NetAllocKindStack     = "stack"     // a per-stack network
	NetAllocKindGateway   = "gateway"   // the shared reverse-proxy network
	NetAllocKindOverlay   = "overlay"   // a per-workspace swarm overlay
	// NetAllocKindReserved marks a subnet that is not Miabi's but overlaps the pool
	// (a pre-existing/unmanaged Docker network), so the allocator never hands it out.
	NetAllocKindReserved = "reserved"
)

// NetworkAllocation pins one Docker network to a subnet carved from the platform pool
// (MIABI_NETWORK_POOL_CIDR) — the ledger the allocator consults for non-overlapping subnets.
// Keyed by DockerName; reserved rows use the sentinel "reserved:<subnet>".
type NetworkAllocation struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// DockerName is the globally-unique Docker network name this subnet is pinned to.
	DockerName string `json:"docker_name" gorm:"uniqueIndex;not null"`
	// Subnet is the allocated CIDR (e.g. 10.42.3.0/24); Gateway is its first host.
	Subnet  string `json:"subnet" gorm:"uniqueIndex;not null"`
	Gateway string `json:"gateway" gorm:"not null"`
	// NodeID is the node the network lives on (0 = local control-plane node). The
	// same subnet may recur across nodes for the same logical network — bridges are
	// node-local — so uniqueness is on DockerName/Subnet, not (Subnet, NodeID).
	NodeID uint `json:"node_id" gorm:"not null;default:0"`
	// Kind labels the allocation (see NetAllocKind*).
	Kind      string    `json:"kind" gorm:"not null;default:workspace"`
	CreatedAt time.Time `json:"created_at"`
}
