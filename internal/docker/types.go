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

package docker

import (
	stackdocker "github.com/miabi-io/miabi/pkg/stack/docker"
)

// Miabi's own types: Docker SDK types never cross this package boundary.

// Aliased from the stack module because Client must use identical named types to satisfy stackdocker.Client.
type (
	Container        = stackdocker.Container
	ContainerNetwork = stackdocker.ContainerNetwork
	Port             = stackdocker.Port
	ContainerConfig  = stackdocker.ContainerConfig
	ContainerMount   = stackdocker.ContainerMount
	PortMapping      = stackdocker.PortMapping
	RunSpec          = stackdocker.RunSpec
	BindMount        = stackdocker.BindMount
	FileEntry        = stackdocker.FileEntry
	GPURequest       = stackdocker.GPURequest
	HealthcheckSpec  = stackdocker.HealthcheckSpec
	NetworkSpec      = stackdocker.NetworkSpec
	NetworkState     = stackdocker.NetworkState
	Volume           = stackdocker.Volume
	RegistryAuth     = stackdocker.RegistryAuth
)

// Info summarizes the Docker engine.
type Info struct {
	// Name is the Docker host's hostname, not the Miabi container's.
	Name          string `json:"name"`
	Version       string `json:"version"`
	APIVersion    string `json:"api_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Containers    int    `json:"containers"`
	ContainersRun int    `json:"containers_running"`
	Images        int    `json:"images"`
	CPUs          int    `json:"cpus"`
	MemTotal      int64  `json:"mem_total"`
	// DataRoot's filesystem size is how a node's storage capacity is measured.
	DataRoot string `json:"data_root,omitempty"`
	// The "nvidia" runtime is present exactly when the NVIDIA Container Toolkit is installed.
	Runtimes []string `json:"runtimes,omitempty"`
}

type Capabilities struct {
	APIVersion string `json:"api_version"`
	// EngineVersion is the daemon's reported server version (e.g. "27.5.1").
	EngineVersion string `json:"engine_version"`
	// OS / Arch are the daemon's platform ("linux", "x86_64"/"aarch64").
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	SwarmActive  bool   `json:"swarm_active"`
	SwarmManager bool   `json:"swarm_manager"`
}

type EngineEvent struct {
	Action      string // start, die, oom, kill, stop, "health_status: healthy", ...
	ContainerID string
	Attributes  map[string]string
}

// VolumeSpec empty Driver uses Docker's local default; DriverOpts carry the NFS/CIFS mount options.
type VolumeSpec struct {
	Name       string
	Labels     map[string]string
	SizeBytes  int64
	Driver     string
	DriverOpts map[string]string
	// DevicePath keeps the volume an ordinary named Docker volume, so backup, housekeeping and mounts are unchanged.
	DevicePath string
}

// Network summarizes a Docker network.
type Network struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Driver string            `json:"driver"`
	Scope  string            `json:"scope"`
	Labels map[string]string `json:"labels,omitempty"`
	// Subnet lets the allocator reserve pool subnets already used by existing networks.
	Subnet  string `json:"subnet,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	// EnableIPv6 is what the daemon reports, honest for networks created before Miabi set it explicitly.
	IPv6Subnet  string `json:"ipv6_subnet,omitempty"`
	IPv6Gateway string `json:"ipv6_gateway,omitempty"`
	EnableIPv6  bool   `json:"enable_ipv6"`
}

// LogLine is a single demultiplexed log line.
type LogLine struct {
	Stream string `json:"stream"` // "stdout" | "stderr"
	Text   string `json:"text"`
}

// StatsSample is a point-in-time resource usage sample for a container.
type StatsSample struct {
	CPUPercent     float64 `json:"cpu_percent"`
	MemoryUsage    uint64  `json:"memory_usage_bytes"`
	MemoryLimit    uint64  `json:"memory_limit_bytes"`
	MemoryPercent  float64 `json:"memory_percent"`
	NetworkRxBytes uint64  `json:"network_rx_bytes"`
	NetworkTxBytes uint64  `json:"network_tx_bytes"`
}

type ProcessList struct {
	Titles    []string   `json:"titles"`
	Processes [][]string `json:"processes"`
}

type Image struct {
	ID          string            `json:"id"`
	RepoTags    []string          `json:"repo_tags,omitempty"`
	RepoDigests []string          `json:"repo_digests,omitempty"`
	Size        int64             `json:"size"`
	SharedSize  int64             `json:"shared_size"`
	Created     int64             `json:"created"`
	Containers  int64             `json:"containers"` // -1 when the daemon did not compute it
	Dangling    bool              `json:"dangling"`   // untagged (<none>:<none>)
	Labels      map[string]string `json:"labels,omitempty"`
}

// ImageInspect Digest falls back to the image ID for a locally-built, never-pushed image.
type ImageInspect struct {
	ID     string `json:"id"`     // content-addressable image ID (sha256:…)
	Digest string `json:"digest"` // repo digest when present, else the image ID
	Size   int64  `json:"size"`
}

type DiskUsage struct {
	Images     DiskUsageCategory `json:"images"`
	Containers DiskUsageCategory `json:"containers"`
	Volumes    DiskUsageCategory `json:"volumes"`
	BuildCache DiskUsageCategory `json:"build_cache"`
	// VolumeItems is free (the daemon walks the fs anyway) but unserialized; callers want totals.
	VolumeItems []VolumeUsage `json:"-"`
}

// RefCount 0 means reclaimable.
type VolumeUsage struct {
	DockerName string `json:"docker_name"`
	Bytes      int64  `json:"bytes"`
	RefCount   int    `json:"ref_count"`
}

// Reclaimable is an upper bound that ignores layer sharing, as `docker system df` reports it.
type DiskUsageCategory struct {
	Count       int   `json:"count"`
	Active      int   `json:"active"`
	TotalBytes  int64 `json:"total_bytes"`
	Reclaimable int64 `json:"reclaimable_bytes"`
}

type PruneImagesOptions struct {
	Dangling bool
	Until    string
}

// PruneReport is the outcome of a prune: what was removed and the bytes freed.
type PruneReport struct {
	ItemsDeleted   []string `json:"items_deleted,omitempty"`
	SpaceReclaimed int64    `json:"space_reclaimed"`
}
