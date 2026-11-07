// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

// Package docker is the stack engine's Docker seam; no Docker SDK types cross it.
package docker

import (
	"strings"
	"time"
)

// Container is a summary of a Docker container.
type Container struct {
	ID           string             `json:"id"`
	Names        []string           `json:"names"`
	Image        string             `json:"image"`
	State        string             `json:"state"`
	Status       string             `json:"status"`
	Health       string             `json:"health,omitempty"` // "", "starting", "healthy", "unhealthy"
	Restarting   bool               `json:"restarting,omitempty"`
	RestartCount int                `json:"restart_count,omitempty"`
	ExitCode     int                `json:"exit_code,omitempty"`
	StartedAt    string             `json:"started_at,omitempty"` // RFC3339 from inspect
	Created      int64              `json:"created"`
	Ports        []Port             `json:"ports,omitempty"`
	Labels       map[string]string  `json:"labels,omitempty"`
	Networks     []ContainerNetwork `json:"networks,omitempty"` // populated by InspectContainer
}

// IPs change on recreate; prefer the network alias for stable addressing.
type ContainerNetwork struct {
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	// Shows whether a container actually got a v6 address; ones started before IPv6 was enabled have none.
	IPv6Address string `json:"ipv6_address,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
	// The only stable address, so moving networks must carry aliases verbatim rather than recompute them.
	Aliases []string `json:"aliases,omitempty"`
}

// Port maps a container port to a host port.
type Port struct {
	PrivatePort uint16 `json:"private_port"`
	PublicPort  uint16 `json:"public_port,omitempty"`
	Protocol    string `json:"protocol"`
}

type ContainerConfig struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"` // c.Config.Image (repo[:tag])
	State         string            `json:"state"`
	Command       []string          `json:"command,omitempty"`
	Entrypoint    []string          `json:"entrypoint,omitempty"`
	Env           []string          `json:"env,omitempty"`  // KEY=VALUE (effective)
	User          string            `json:"user,omitempty"` // c.Config.User: "uid[:gid]" or a name; "" = image default
	Labels        map[string]string `json:"labels,omitempty"`
	Ports         []PortMapping     `json:"ports,omitempty"`    // published host ports
	Mounts        []ContainerMount  `json:"mounts,omitempty"`   // volume + bind mounts
	Networks      []string          `json:"networks,omitempty"` // attached network names
	RestartPolicy string            `json:"restart_policy"`     // no|always|unless-stopped|on-failure[:N]
	MemoryBytes   int64             `json:"memory_bytes"`
	NanoCPUs      int64             `json:"nano_cpus"`
}

type ContainerMount struct {
	Type        string `json:"type"` // "volume" | "bind" | ...
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"read_only"`
}

type PortMapping struct {
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
	HostPort      int    `json:"host_port,omitempty"`
}

// RunSpec describes a container to create and start.
type RunSpec struct {
	Name           string
	Image          string
	Hostname       string   // container hostname (uname -n); blank = Docker default
	Env            []string // KEY=VALUE
	Entrypoint     []string // optional entrypoint override
	Cmd            []string // optional command override
	WorkingDir     string
	Labels         map[string]string
	Networks       []string // networks to attach
	NetworkMode    string
	NetworkAliases []string
	// Exposes the service name on the stack network without leaking it onto the shared gateway network.
	AliasesByNetwork map[string][]string
	// Ports maps "containerPort/proto" -> "hostPort" (e.g. "80/tcp" -> "8080").
	Ports map[string]string
	// Keys match Ports; a missing or empty entry means all interfaces.
	PortBindIPs map[string]string
	// Mounts maps volume name -> container path.
	Mounts map[string]string
	// Copy-up would re-apply the image's ownership on every start, undoing the restricted-profile chown.
	NoCopyVolumes bool
	// A container that only reads the data it archives should not hold a writable handle to it.
	ReadOnlyMounts []string
	// Only for allow-listed privileged mounts; the source is server-resolved, never client input.
	Binds []BindMount
	// Resource limits (0 = unlimited).
	MemoryBytes int64
	NanoCPUs    int64
	// Count -1 means all GPUs (used by the inventory probe).
	GPUs []GPURequest
	// Empty defaults to "unless-stopped".
	RestartPolicy string
	// Healthcheck is the container health probe; nil disables it.
	Healthcheck     *HealthcheckSpec
	User            string
	NoNewPrivileges bool
	CapDrop         []string
	// Allow-listed before reaching here (models.NormalizeCapabilities / NormalizeDevices).
	CapAdd  []string
	Devices []string
	// ReadOnlyRootfs mounts the container's root filesystem read-only (--read-only).
	ReadOnlyRootfs bool
	// Needed for the docker group so a non-root control plane can read the socket, like compose's group_add.
	GroupAdd []string
	// Written between create and start, so no host path or distribution is needed; re-applied on every deploy.
	Files []FileEntry
}

// Mode is a 3- or 4-digit octal string; empty means 0644.
type FileEntry struct {
	Path    string
	Content string
	Mode    string
}

// BindMount is a host path bound into a container.
type BindMount struct {
	Source   string // host path
	Target   string // container path
	ReadOnly bool
	// An unmounted storage-class disk must fail loudly rather than get a directory on the root filesystem.
	NoCreate bool
}

// Count -1 means all; Capabilities is [["gpu"]] for NVIDIA.
type GPURequest struct {
	DeviceIDs    []string   // resolved GPU UUIDs; nil selects by Count instead
	Count        int        // used only when DeviceIDs is nil (-1 = all devices)
	Capabilities [][]string // e.g. [["gpu"]]
}

// HealthcheckSpec configures a container healthcheck (Docker HEALTHCHECK).
type HealthcheckSpec struct {
	Test        []string // e.g. ["CMD-SHELL", "curl -f http://localhost/ || exit 1"]
	Interval    time.Duration
	Timeout     time.Duration
	Retries     int
	StartPeriod time.Duration
}

// NetworkState is the part of an existing network the stack reconciles against its manifest.
type NetworkState struct {
	ID         string
	EnableIPv6 bool
}

// Explicit IPAM so creation does not draw from Docker's default-address-pools.
type NetworkSpec struct {
	Name       string
	Driver     string // "" = bridge; "overlay" for swarm
	Internal   bool
	Attachable bool // overlay networks that standalone containers may join
	Encrypted  bool // overlay data-plane encryption
	Subnet     string
	Gateway    string
	Labels     map[string]string
	EnableIPv6 *bool
	IPv6Subnet string
}

// Volume summarizes a Docker volume.
type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	CreatedAt  string            `json:"created_at,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// RegistryAuth holds credentials for pulling from a private registry.
type RegistryAuth struct {
	Server   string // registry host, e.g. registry-1.docker.io, ghcr.io
	Username string
	Password string
}

// IsRootless reports whether a daemon's `docker info` security options mark it rootless.
func IsRootless(securityOptions []string) bool {
	for _, o := range securityOptions {
		for _, kv := range strings.Split(o, ",") {
			if kv == "name=rootless" {
				return true
			}
		}
	}
	return false
}
