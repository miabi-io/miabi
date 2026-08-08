// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	stackdocker "github.com/miabi-io/miabi/pkg/stack/docker"
)

// These are Miabi's own types: the Docker SDK's types never cross this
// package boundary, keeping services and handlers decoupled from the SDK.

// The types the stack engine also speaks are owned by the stack module and aliased here, so both
// sides use one definition — Go's structural typing needs identical named types for Client to
// satisfy stackdocker.Client. Aliases are transparent, so every call site is unaffected.
type (
	Container        = stackdocker.Container
	ContainerNetwork = stackdocker.ContainerNetwork
	Port             = stackdocker.Port
	ContainerConfig  = stackdocker.ContainerConfig
	ContainerMount   = stackdocker.ContainerMount
	PortMapping      = stackdocker.PortMapping
	RunSpec          = stackdocker.RunSpec
	BindMount        = stackdocker.BindMount
	GPURequest       = stackdocker.GPURequest
	HealthcheckSpec  = stackdocker.HealthcheckSpec
	NetworkSpec      = stackdocker.NetworkSpec
	Volume           = stackdocker.Volume
	RegistryAuth     = stackdocker.RegistryAuth
)

// Info summarizes the Docker engine.
type Info struct {
	// Name is the Docker host's hostname (the actual machine name, not the
	// Miabi container's), as reported by the daemon.
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
	// Runtimes are the container runtimes the daemon advertises (docker info). The "nvidia" key is
	// present exactly when the NVIDIA Container Toolkit is installed, so it is how the control plane
	// decides a node is GPU-capable before ever probing it.
	Runtimes []string `json:"runtimes,omitempty"`
}

// EngineEvent is a Docker daemon event for a managed container. Attributes
// carries the container's labels (e.g. "io.miabi.app") and event metadata
// (e.g. "exitCode").
type EngineEvent struct {
	Action      string // start, die, oom, kill, stop, "health_status: healthy", ...
	ContainerID string
	Attributes  map[string]string
}

// VolumeSpec describes a managed volume to create, including an optional driver and driver options
// for shared storage (NFS/CIFS). An empty Driver uses Docker's default (local). DriverOpts are the
// backend mount options (e.g. NFS: type=nfs, o=addr=...,rw, device=:/export).
type VolumeSpec struct {
	Name       string
	Labels     map[string]string
	SizeBytes  int64
	Driver     string
	DriverOpts map[string]string
}

// Network summarizes a Docker network.
type Network struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Driver string            `json:"driver"`
	Scope  string            `json:"scope"`
	Labels map[string]string `json:"labels,omitempty"`
	// Subnet is the network's first IPAM subnet (CIDR), empty if unset. Used by the
	// subnet allocator to reserve pool subnets already in use by existing networks.
	Subnet string `json:"subnet,omitempty"`
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

// ProcessList is the running processes in a container (the "docker top" view):
// column titles and one row of cells per process. Sourced from the host's ps, so
// it works even for images that ship no ps binary.
type ProcessList struct {
	Titles    []string   `json:"titles"`
	Processes [][]string `json:"processes"`
}

// Image summarizes a local Docker image, with the references and usage signals
// the housekeeping report needs (whether a container uses it, whether it is
// dangling/untagged).
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

// ImageInspect is a built or pulled image's identity and size. Digest is the registry repo-digest
// when the image carries one; for a locally-built, never-pushed image it falls back to the
// content-addressable image ID, a stable local handle for deploy-by-digest on a single node.
type ImageInspect struct {
	ID     string `json:"id"`     // content-addressable image ID (sha256:…)
	Digest string `json:"digest"` // repo digest when present, else the image ID
	Size   int64  `json:"size"`
}

// DiskUsage is a `docker system df`-style breakdown for a node: per-category
// counts and total vs reclaimable bytes. It drives the housekeeping report.
type DiskUsage struct {
	Images     DiskUsageCategory `json:"images"`
	Containers DiskUsageCategory `json:"containers"`
	Volumes    DiskUsageCategory `json:"volumes"`
	BuildCache DiskUsageCategory `json:"build_cache"`
}

// VolumeUsage is one Docker volume's measured on-disk size, keyed by name so it
// joins to a workspace's Volume rows. RefCount 0 = reclaimable.
type VolumeUsage struct {
	DockerName string `json:"docker_name"`
	Bytes      int64  `json:"bytes"`
	RefCount   int    `json:"ref_count"`
}

// DiskUsageCategory is one row of the disk-usage breakdown. Reclaimable is an
// upper-bound estimate (it ignores layer sharing between images), matching how
// `docker system df` reports it.
type DiskUsageCategory struct {
	Count       int   `json:"count"`
	Active      int   `json:"active"`
	TotalBytes  int64 `json:"total_bytes"`
	Reclaimable int64 `json:"reclaimable_bytes"`
}

// PruneImagesOptions selects what an image prune targets. Dangling restricts it to untagged
// (`<none>`) images, which are always safe to remove; the all-unused mode requires the caller's
// referenced-image guard and is not used by the safe default flow.
type PruneImagesOptions struct {
	Dangling bool   // true: only dangling images; false: all unused images
	Until    string // optional max-age filter (Go duration, e.g. "168h"); "" = no age limit
}

// PruneReport is the outcome of a prune: what was removed and the bytes freed.
type PruneReport struct {
	ItemsDeleted   []string `json:"items_deleted,omitempty"`
	SpaceReclaimed int64    `json:"space_reclaimed"`
}
