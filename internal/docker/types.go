// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import "time"

// These are Miabi's own types: the Docker SDK's types never cross this
// package boundary, keeping services and handlers decoupled from the SDK.

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
	// Runtimes are the container runtimes the daemon advertises (docker info).
	// The "nvidia" key is present exactly when the NVIDIA Container Toolkit is
	// installed, so it is how the control plane decides a node is GPU-capable
	// before ever probing it.
	Runtimes []string `json:"runtimes,omitempty"`
}

// Port maps a container port to a host port.
type Port struct {
	PrivatePort uint16 `json:"private_port"`
	PublicPort  uint16 `json:"public_port,omitempty"`
	Protocol    string `json:"protocol"`
}

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

// ContainerNetwork is a container's attachment to a Docker network (its
// in-network IP). IPs are ephemeral — they change when the container is
// recreated; prefer the network alias for stable addressing.
type ContainerNetwork struct {
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	Gateway   string `json:"gateway,omitempty"`
}

// ContainerConfig is the full inspected configuration of a container, used by
// the import flow to adopt a pre-existing container as a Miabi app. It
// carries the runtime fields a summary Container omits (env, mounts, command,
// limits, restart policy).
type ContainerConfig struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"` // c.Config.Image (repo[:tag])
	State         string            `json:"state"`
	Command       []string          `json:"command,omitempty"`
	Entrypoint    []string          `json:"entrypoint,omitempty"`
	Env           []string          `json:"env,omitempty"` // KEY=VALUE (effective)
	Labels        map[string]string `json:"labels,omitempty"`
	Ports         []PortMapping     `json:"ports,omitempty"`    // published host ports
	Mounts        []ContainerMount  `json:"mounts,omitempty"`   // volume + bind mounts
	Networks      []string          `json:"networks,omitempty"` // attached network names
	RestartPolicy string            `json:"restart_policy"`     // no|always|unless-stopped|on-failure[:N]
	MemoryBytes   int64             `json:"memory_bytes"`
	NanoCPUs      int64             `json:"nano_cpus"`
}

// PortMapping is a container port and the host port it is published on (0 = not
// published).
type PortMapping struct {
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
	HostPort      int    `json:"host_port,omitempty"`
}

// ContainerMount is a mount on an inspected container. For Type "volume", Name
// is the Docker volume name; Source is the host path for binds.
type ContainerMount struct {
	Type        string `json:"type"` // "volume" | "bind" | ...
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"read_only"`
}

// BindMount is a host path bound into a container.
type BindMount struct {
	Source   string // host path
	Target   string // container path
	ReadOnly bool
}

// RunSpec describes a container to create and start.
type RunSpec struct {
	Name       string
	Image      string
	Hostname   string   // container hostname (uname -n); blank = Docker default
	Env        []string // KEY=VALUE
	Entrypoint []string // optional entrypoint override
	Cmd        []string // optional command override
	// WorkingDir sets the container's working directory (Docker WORKDIR). Blank
	// uses the image default. Pipeline steps set it to the shared workspace
	// (/workspace) so commands run against the checked-out repository.
	WorkingDir string
	Labels     map[string]string
	Networks   []string // networks to attach
	// NetworkAliases are DNS aliases applied on each attached network, giving
	// the container a stable name the reverse proxy can target across deploys.
	NetworkAliases []string
	// AliasesByNetwork sets extra DNS aliases on specific networks only (network
	// name -> aliases). Used to expose an app by its service name within its
	// stack network without leaking that name onto the shared gateway network.
	AliasesByNetwork map[string][]string
	// Ports maps "containerPort/proto" -> "hostPort" (e.g. "80/tcp" -> "8080").
	Ports map[string]string
	// PortBindIPs optionally maps "containerPort/proto" -> host bind IP, to publish
	// a port on a specific interface (e.g. a node's private address) instead of
	// 0.0.0.0. Keys match Ports; a missing/empty entry means all interfaces.
	PortBindIPs map[string]string
	// Mounts maps volume name -> container path.
	Mounts map[string]string
	// Binds are host path -> container path bind mounts. Used only for
	// allow-listed privileged host mounts (e.g. the Docker socket); the source
	// is a server-resolved host path, never client input.
	Binds []BindMount
	// Resource limits (0 = unlimited).
	MemoryBytes int64
	NanoCPUs    int64
	// GPUs requests whole-device GPU access via the NVIDIA runtime. Empty = none.
	// Each entry either pins specific devices by UUID (DeviceIDs) or asks for N
	// of any kind (Count); -1 Count means "all GPUs" (used by the inventory probe).
	GPUs []GPURequest
	// RestartPolicy is the Docker restart policy ("no", "always",
	// "unless-stopped", "on-failure"). Empty defaults to "unless-stopped".
	RestartPolicy string
	// Healthcheck is the container health probe; nil disables it.
	Healthcheck *HealthcheckSpec
	// Container security (restricted security profile). User overrides the user
	// the container runs as ("uid", "uid:gid", or a name); empty = the image's
	// default user. NoNewPrivileges sets the no-new-privileges securityOpt;
	// CapDrop drops the listed Linux capabilities (e.g. "NET_RAW").
	User            string
	NoNewPrivileges bool
	CapDrop         []string
}

// GPURequest describes a set of GPU devices to attach to a container via the
// NVIDIA runtime (Docker DeviceRequests). Either DeviceIDs (resolved GPU UUIDs)
// pins exact cards, or Count asks for N-any-of-kind (-1 = all). Capabilities is
// the driver capability set, [["gpu"]] for NVIDIA.
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

// EngineEvent is a Docker daemon event for a managed container. Attributes
// carries the container's labels (e.g. "io.miabi.app") and event metadata
// (e.g. "exitCode").
type EngineEvent struct {
	Action      string // start, die, oom, kill, stop, "health_status: healthy", ...
	ContainerID string
	Attributes  map[string]string
}

// RegistryAuth holds credentials for pulling from a private registry.
type RegistryAuth struct {
	Server   string // registry host, e.g. registry-1.docker.io, ghcr.io
	Username string
	Password string
}

// VolumeSpec describes a managed volume to create, including an optional driver
// and driver options for shared storage (NFS/CIFS). An empty Driver uses
// Docker's default (local) driver. DriverOpts are the backend mount options
// (e.g. NFS: type=nfs, o=addr=…,rw, device=:/export).
type VolumeSpec struct {
	Name       string
	Labels     map[string]string
	SizeBytes  int64
	Driver     string
	DriverOpts map[string]string
}

// Volume summarizes a Docker volume.
type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	CreatedAt  string            `json:"created_at,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
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

// NetworkSpec describes a managed network to create. An empty Driver defaults to
// "bridge". Subnet/Gateway, when set, are passed as explicit IPAM so creation
// does not draw from Docker's built-in default-address-pools.
type NetworkSpec struct {
	Name       string
	Driver     string // "" = bridge; "overlay" for swarm
	Internal   bool
	Attachable bool // overlay networks that standalone containers may join
	Encrypted  bool // overlay data-plane encryption
	Subnet     string
	Gateway    string
	Labels     map[string]string
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

// ImageInspect is a built or pulled image's identity and size, returned by
// InspectImage. Digest is the registry repo-digest (sha256:…) when the image
// carries one; for a locally-built, never-pushed image it falls back to the
// content-addressable image ID, which is a stable local handle for deploy-by-
// digest on a single node.
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

// PruneImagesOptions selects what an image prune targets. Dangling restricts the
// prune to untagged (`<none>`) images, which are always safe to remove; the
// all-unused mode requires the caller's referenced-image guard and is not used
// by the safe default flow.
type PruneImagesOptions struct {
	Dangling bool   // true: only dangling images; false: all unused images
	Until    string // optional max-age filter (Go duration, e.g. "168h"); "" = no age limit
}

// PruneReport is the outcome of a prune: what was removed and the bytes freed.
type PruneReport struct {
	ItemsDeleted   []string `json:"items_deleted,omitempty"`
	SpaceReclaimed int64    `json:"space_reclaimed"`
}
