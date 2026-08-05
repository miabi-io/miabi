// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// ServerAccessMode is how the control plane reaches a node's Docker engine.
// Distinct from ServerConnectivity (how the proxy reaches apps).
type ServerAccessMode string

const (
	// AccessSocket: a local/Unix (or custom) Docker host where the control plane
	// runs (DockerEndpoint, e.g. unix:///var/run/docker.sock). The control-plane
	// node uses this.
	AccessSocket ServerAccessMode = "socket"
	// AccessAgent: the node's agent dials in over a tunnel (NAT-friendly, no
	// inbound ports on the node). DockerEndpoint is unused.
	AccessAgent ServerAccessMode = "agent"
	// AccessAPI: the control plane dials the node's Docker API over TCP
	// (DockerEndpoint = tcp://host:2376), optionally with TLS/mTLS.
	AccessAPI ServerAccessMode = "api"
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
	// RoleManager is the control-plane node that runs Miabi itself (the
	// platform manager). There is exactly one, and it reaches Docker via its local
	// socket.
	RoleManager ServerRole = "manager"
	// RoleNode is a worker node that only runs workloads.
	RoleNode ServerRole = "node"
)

// ServerConnectivity is how the platform reaches apps running on a node.
type ServerConnectivity string

const (
	// ConnectivityPortForward: the central proxy forwards to the node's published
	// host port (http://<address>:<hostPort>). For trusted/private networks.
	ConnectivityPortForward ServerConnectivity = "port-forward"
	// ConnectivityEdgeGateway: the node runs its own gateway (public ingress, its
	// own TLS) that pulls its routes from the control plane's HTTP provider.
	ConnectivityEdgeGateway ServerConnectivity = "edge-gateway"
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

// Server is a Docker host (node). The local node runs alongside the control
// plane; remote nodes run a `miabi agent` that dials in and exposes their
// Docker engine over a tunnel.
type Server struct {
	UIDModel
	ID           uint               `json:"id" gorm:"primaryKey"`
	Name         string             `json:"name" gorm:"uniqueIndex;not null"`
	DisplayName  string             `json:"display_name"`
	Connectivity ServerConnectivity `json:"connectivity" gorm:"not null;default:port-forward"`
	// AccessMode is how the control plane reaches this node's Docker engine.
	// Existing rows backfill to "agent" (column default); the local node is set
	// to "socket" at bootstrap.
	AccessMode ServerAccessMode `json:"access_mode" gorm:"not null;default:agent"`
	// DockerEndpoint is the Docker host for non-agent modes: unix://… (socket) or
	// tcp://host:2376 (api). Unused for agent.
	DockerEndpoint string `json:"docker_endpoint"`
	IsLocal        bool   `json:"is_local" gorm:"not null;default:false"`
	// Role is the node's cluster role: "manager" for the control-plane node,
	// "node" for workers.
	Role       ServerRole   `json:"role" gorm:"not null;default:node"`
	Status     ServerStatus `json:"status" gorm:"not null;default:unknown"`
	LastSeenAt *time.Time   `json:"last_seen_at"`

	// api (TCP) TLS material — optional (plaintext when empty). PEM-encoded; the
	// private key is encrypted at rest and never serialized.
	TLSCACert string `json:"-" gorm:"type:text"`
	TLSCert   string `json:"-" gorm:"type:text"`
	TLSKeyEnc string `json:"-" gorm:"column:tls_key_enc;type:text"`
	// TLSEnabled is a transient flag for responses (whether TLS material is set).
	TLSEnabled bool `json:"tls_enabled" gorm:"-"`

	// Remote-node fields.
	// Address is the host/IP the reverse proxy uses to reach this node's
	// published app ports (e.g. "10.0.0.7").
	Address string `json:"address,omitempty"`

	PublicIP       string `json:"public_ip,omitempty"`
	PublicHostname string `json:"public_hostname,omitempty"`
	// TokenHash is the SHA-256 of the agent join token; the plaintext is shown
	// once at creation and never stored.
	TokenHash       string `json:"-" gorm:"index"`
	GatewayTokenEnc string `json:"-"`
	// GatewayDeployedAt is when the node's gateway was last deployed (display).
	GatewayDeployedAt *time.Time `json:"gateway_deployed_at,omitempty"`
	GatewayConfigYAML string     `json:"-" gorm:"type:text"`
	// GatewayImage overrides the edge-gateway image/tag for this node; empty =
	// the resolved catalog/default image.
	GatewayImage     string `json:"gateway_image,omitempty"`
	GatewayContainer string `json:"gateway_container,omitempty"`
	// GatewayImported marks that the node's gateway was adopted from a pre-existing
	// container rather than deployed by Miabi.
	GatewayImported         bool                   `json:"gateway_imported" gorm:"not null;default:false"`
	GatewayRedisPasswordEnc string                 `json:"-"`
	GatewayUpdate           *GatewayUpdateProgress `json:"gateway_update,omitempty" gorm:"serializer:json"`
	// AgentConnected reflects a live agent tunnel (transient; set by the
	// connection manager).
	AgentConnected bool              `json:"agent_connected" gorm:"-"`
	AgentVersion   string            `json:"agent_version,omitempty"`
	Cordoned       bool              `json:"cordoned" gorm:"not null;default:false"`
	Labels         map[string]string `json:"labels,omitempty" gorm:"serializer:json"`
	SwarmNodeID    string            `json:"swarm_node_id,omitempty" gorm:"index"`
	AutoJoined     bool              `json:"auto_joined" gorm:"not null;default:false"`
	SwarmRole      string            `json:"swarm_role,omitempty" gorm:"-"`
	// SwarmAvailability is the scheduling availability: active | pause | drain.
	SwarmAvailability string `json:"swarm_availability,omitempty" gorm:"-"`
	// SwarmState is the swarm-reported reachability: ready | down | unknown |
	// disconnected.
	SwarmState string `json:"swarm_state,omitempty" gorm:"-"`
	// InSwarm reports whether this node is currently a member of the swarm.
	InSwarm bool `json:"in_swarm" gorm:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GatewayUpdateProgress is the live state of a safe edge-gateway update: the new
// image is first started as a throwaway test container with the same config and
// volumes, observed for a grace period, then promoted to replace the live
// gateway. The admin watches these phases on the node detail page. Mirrors
// UpgradeProgress (database version upgrades).
type GatewayUpdateProgress struct {
	FromImage string `json:"from_image"`
	ToImage   string `json:"to_image"`
	// Phase: queued | pulling | testing | observing | promoting | verifying |
	// done | failed.
	Phase string `json:"phase"`
	Error string `json:"error,omitempty"`
}
