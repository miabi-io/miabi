// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package docker is the Docker seam the platform-stack engine talks through. It declares the
// narrow client interface the engine needs and the types that cross it — deliberately none of
// them from the Docker SDK, so a caller may back this with any implementation.
package docker

import "time"

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
	// Aliases are the container's DNS aliases on this network — the only stable way to address it, so
	// moving a container between networks (see the bridge -> overlay migration in services/network)
	// must carry them across verbatim rather than recomputing them.
	Aliases []string `json:"aliases,omitempty"`
}

// Port maps a container port to a host port.
type Port struct {
	PrivatePort uint16 `json:"private_port"`
	PublicPort  uint16 `json:"public_port,omitempty"`
	Protocol    string `json:"protocol"`
}

// ContainerConfig is the full inspected configuration of a container, used by the import flow to
// adopt a pre-existing container as a Miabi app. It carries the runtime fields a summary
// Container omits (env, mounts, command, limits, restart policy).
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

// ContainerMount is a mount on an inspected container. For Type "volume", Name
// is the Docker volume name; Source is the host path for binds.
type ContainerMount struct {
	Type        string `json:"type"` // "volume" | "bind" | ...
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"read_only"`
}

// PortMapping is a container port and the host port it is published on (0 = not
// published).
type PortMapping struct {
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
	HostPort      int    `json:"host_port,omitempty"`
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
	// NoCopyVolumes disables Docker's copy-up (image->volume seeding) for the named volumes in Mounts.
	// Set under the restricted profile, where volumes are already seeded and chowned: copy-up would
	// re-apply the image mount-dir's ownership on every start, leaving the process unable to write.
	NoCopyVolumes bool
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
	// Container security (restricted profile). User overrides the user the container runs as ("uid",
	// "uid:gid", or a name); empty means the image's default. NoNewPrivileges sets that securityOpt;
	// CapDrop drops the listed Linux capabilities (e.g. "NET_RAW").
	User            string
	NoNewPrivileges bool
	CapDrop         []string
	// GroupAdd are supplementary groups (Docker --group-add), by GID or name. The
	// control plane needs the host's "docker" group to read /var/run/docker.sock when
	// it does not run as root — the same thing compose.yaml's `group_add` does.
	GroupAdd []string
}

// BindMount is a host path bound into a container.
type BindMount struct {
	Source   string // host path
	Target   string // container path
	ReadOnly bool
}

// GPURequest describes a set of GPU devices to attach via the NVIDIA runtime (Docker
// DeviceRequests). Either DeviceIDs (resolved GPU UUIDs) pins exact cards, or Count asks for
// N-any-of-kind (-1 = all). Capabilities is the driver capability set, [["gpu"]] for NVIDIA.
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
