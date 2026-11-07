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

import (
	"strings"
	"time"
)

// ServerStatus is the reachability state of a node.
type ServerStatus string

const (
	ServerStatusOnline  ServerStatus = "online"
	ServerStatusOffline ServerStatus = "offline"
	ServerStatusUnknown ServerStatus = "unknown"
)

// ServerAccessMode is how the control plane reaches a node's Docker engine, distinct from ServerConnectivity.
type ServerAccessMode string

const (
	AccessSocket ServerAccessMode = "socket"
	// NAT-friendly: no inbound ports on the node; DockerEndpoint is unused.
	AccessAgent ServerAccessMode = "agent"
	AccessAPI   ServerAccessMode = "api"
)

// ValidAccessMode reports whether m is a known access mode.
func ValidAccessMode(m ServerAccessMode) bool {
	switch m {
	case AccessSocket, AccessAgent, AccessAPI:
		return true
	}
	return false
}

// ServerRole is a node's role in the cluster.
type ServerRole string

const (
	// There is exactly one manager, and it reaches Docker via its local socket.
	RoleManager ServerRole = "manager"
	// RoleNode is a worker node that only runs workloads.
	RoleNode ServerRole = "node"
)

// ServerConnectivity is how the platform reaches apps running on a node.
type ServerConnectivity string

const (
	ConnectivityEdgeGateway ServerConnectivity = "edge-gateway"
	// Served over the overlay for a swarm member; the control-plane node is one too.
	ConnectivityCluster ServerConnectivity = "cluster"
)

func (s *Server) Label() string {
	if s == nil {
		return ""
	}
	if l := strings.TrimSpace(s.DisplayName); l != "" {
		return l
	}
	return s.Name
}

type Server struct {
	UIDModel
	ID             uint               `json:"id" gorm:"primaryKey"`
	Name           string             `json:"name" gorm:"uniqueIndex;not null"`
	DisplayName    string             `json:"display_name"`
	ClusterID      uint               `json:"cluster_id" gorm:"index;not null;default:0"`
	Connectivity   ServerConnectivity `json:"connectivity" gorm:"not null;default:cluster"`
	AccessMode     ServerAccessMode   `json:"access_mode" gorm:"not null;default:agent"`
	DockerEndpoint string             `json:"docker_endpoint"`
	IsLocal        bool               `json:"is_local" gorm:"not null;default:false"`
	Role           ServerRole         `json:"role" gorm:"not null;default:node"`
	Status         ServerStatus       `json:"status" gorm:"not null;default:unknown"`
	LastSeenAt     *time.Time         `json:"last_seen_at"`

	// Plaintext when empty; the private key is encrypted at rest and never serialized.
	TLSCACert string `json:"-" gorm:"type:text"`
	TLSCert   string `json:"-" gorm:"type:text"`
	TLSKeyEnc string `json:"-" gorm:"column:tls_key_enc;type:text"`
	// TLSEnabled is a transient flag for responses (whether TLS material is set).
	TLSEnabled bool `json:"tls_enabled" gorm:"-"`

	// Host/IP the reverse proxy uses to reach this node's published app ports.
	Address string `json:"address,omitempty"`

	// The plaintext is shown once at creation and never stored.
	TokenHash       string `json:"-" gorm:"index"`
	GatewayTokenEnc string `json:"-"`
	// GatewayDeployedAt is when the node's gateway was last deployed (display).
	GatewayDeployedAt       *time.Time             `json:"gateway_deployed_at,omitempty"`
	GatewayConfigYAML       string                 `json:"-" gorm:"type:text"`
	GatewayImage            string                 `json:"gateway_image,omitempty"`
	GatewayContainer        string                 `json:"gateway_container,omitempty"`
	GatewayImported         bool                   `json:"gateway_imported" gorm:"not null;default:false"`
	GatewayRedisPasswordEnc string                 `json:"-"`
	GatewayUpdate           *GatewayUpdateProgress `json:"gateway_update,omitempty" gorm:"serializer:json"`
	Dedicated               bool                   `json:"dedicated" gorm:"-"`
	OrganizationName        string                 `json:"organization_name,omitempty" gorm:"-"`
	ClusterName             string                 `json:"cluster_name,omitempty" gorm:"-"`
	AgentConnected          bool                   `json:"agent_connected" gorm:"-"`
	AgentVersion            string                 `json:"agent_version,omitempty"`
	AgentState              string                 `json:"agent_state,omitempty" gorm:"-"`
	AgentLatestVersion      string                 `json:"agent_latest_version,omitempty" gorm:"-"`
	EngineVersion           string                 `json:"engine_version,omitempty"`
	Cordoned                bool                   `json:"cordoned" gorm:"not null;default:false"`
	Labels                  map[string]string      `json:"labels,omitempty" gorm:"serializer:json"`
	SwarmNodeID             string                 `json:"swarm_node_id,omitempty" gorm:"index"`
	AutoJoined              bool                   `json:"auto_joined" gorm:"not null;default:false"`
	SwarmRole               string                 `json:"swarm_role,omitempty" gorm:"-"`
	// SwarmAvailability is the scheduling availability: active | pause | drain.
	SwarmAvailability string `json:"swarm_availability,omitempty" gorm:"-"`
	SwarmState        string `json:"swarm_state,omitempty" gorm:"-"`
	// InSwarm reports whether this node is currently a member of the swarm.
	InSwarm bool `json:"in_swarm" gorm:"-"`

	// Docker's figures are cgroup-aware, so unlike /proc they describe the node even inside a container or limited VM.
	CPUCores         int   `json:"cpu_cores" gorm:"not null;default:0"`
	MemoryBytes      int64 `json:"memory_bytes" gorm:"not null;default:0"`
	StorageBytes     int64 `json:"storage_bytes" gorm:"not null;default:0"`
	StorageFreeBytes int64 `json:"storage_free_bytes" gorm:"not null;default:0"`
	// nil means never measured, not zero capacity: totals skip it rather than adding zero.
	CapacityMeasuredAt *time.Time `json:"capacity_measured_at,omitempty"`

	// A node whose /proc reports its physical host is left unmeasured rather than counting another machine's load.
	CPUPercent      float64    `json:"cpu_percent" gorm:"not null;default:0"`
	MemUsedBytes    int64      `json:"mem_used_bytes" gorm:"not null;default:0"`
	UsageMeasuredAt *time.Time `json:"usage_measured_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GatewayUpdateProgress: the new image runs as a throwaway test container and is observed before promotion.
type GatewayUpdateProgress struct {
	FromImage string `json:"from_image"`
	ToImage   string `json:"to_image"`
	Phase     string `json:"phase"`
	Error     string `json:"error,omitempty"`
}
