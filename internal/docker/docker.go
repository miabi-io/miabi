// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package docker is the sole adapter over the Docker Engine SDK. Services depend
// on the Client interface (not the SDK), keeping the rest of the codebase free
// of Docker SDK types.
package docker

import (
	"context"
	"errors"
	"io"
	"net"
)

// ErrNotFound is returned when a container/image/volume does not exist.
var ErrNotFound = errors.New("docker: resource not found")

// Client is the abstraction the rest of the app depends on.
type Client interface {
	// Ping verifies the daemon is reachable.
	Ping(ctx context.Context) error
	// Info returns engine information.
	Info(ctx context.Context) (Info, error)

	// Containers.
	ListContainers(ctx context.Context, all bool) ([]Container, error)
	InspectContainer(ctx context.Context, id string) (Container, error)
	// InspectContainerConfig returns the full runtime configuration of a container
	// (env, mounts, command, limits, restart policy) for adopting it as an app.
	InspectContainerConfig(ctx context.Context, id string) (ContainerConfig, error)
	RunContainer(ctx context.Context, spec RunSpec) (string, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeoutSeconds int) error
	RestartContainer(ctx context.Context, id string, timeoutSeconds int) error
	RemoveContainer(ctx context.Context, id string, force bool) error

	// RunOneShot runs a container to completion and returns its exit code and
	// combined logs, then removes it. Used for backup/restore jobs.
	RunOneShot(ctx context.Context, spec RunSpec) (exitCode int, logs string, err error)

	// RunOneShotStream runs a container to completion like RunOneShot, but streams
	// its combined output to sink line-by-line as it runs (live logs) instead of
	// returning the buffered logs. Used by the buildpack build provider.
	RunOneShotStream(ctx context.Context, spec RunSpec, sink func(LogLine) error) (exitCode int, err error)

	// ReadContainerFile reads a single file at path from inside a container's
	// filesystem (size-capped). Used to copy an imported gateway's config.
	ReadContainerFile(ctx context.Context, containerID, path string) ([]byte, error)

	// CopyFileFromVolume reads a single file from a named volume by mounting it
	// into a short-lived (never started) helper container created from image,
	// and returns the file's contents and size. The returned reader must be
	// closed by the caller; closing it also removes the helper container.
	CopyFileFromVolume(ctx context.Context, volume, image, file string) (rc io.ReadCloser, size int64, err error)

	// CopyToVolume streams content (a tar-able single file of the given size) into
	// name inside a named volume, by mounting it into a short-lived helper. Used
	// to land an uploaded dump on a node's backup volume. image must be present.
	CopyToVolume(ctx context.Context, volume, image, name string, content io.Reader, size int64) error

	// DialNetwork opens a raw, bidirectional TCP byte stream to host:port as
	// reachable from containers on the given Docker network. It runs an ephemeral
	// relay (socat, from image) attached to that network and bridges its stdio
	// over the engine connection, so it works the same on a local engine and a
	// remote (tunneled) one — no host port is ever published. image must already
	// be present on the node. Closing the returned conn stops and removes the
	// relay container.
	DialNetwork(ctx context.Context, network, image, host string, port int) (net.Conn, error)

	// Images. auth may be nil for anonymous (public) pulls.
	PullImage(ctx context.Context, ref string, auth *RegistryAuth) error
	// TagImage adds target as an additional tag for the local source image.
	TagImage(ctx context.Context, source, target string) error
	// PushImage pushes a local image ref to its registry. auth may be nil.
	PushImage(ctx context.Context, ref string, auth *RegistryAuth) error
	// BuildImage builds an image from a local context directory, streaming
	// build output to sink, and tags it.
	BuildImage(ctx context.Context, contextDir, dockerfile, tag string, sink func(LogLine) error) error
	// InspectImage returns a local image's identity (ID + digest) and size.
	InspectImage(ctx context.Context, ref string) (ImageInspect, error)
	// ImageExists reports whether ref is present locally (no pull).
	ImageExists(ctx context.Context, ref string) (bool, error)
	// RemoveImage deletes a local image by reference (tag or id). force removes
	// it even if tagged/referenced; a not-found image is treated as success.
	RemoveImage(ctx context.Context, ref string, force bool) error

	// Housekeeping (reclaim & report). ListImages enumerates local images with
	// usage signals; DiskUsage is a `docker system df`-style breakdown. The Prune*
	// calls reclaim disk and report what was freed — they target only the safe set
	// (dangling images, the build cache); callers apply their own managed-resource
	// guards on top.
	ListImages(ctx context.Context) ([]Image, error)
	DiskUsage(ctx context.Context) (DiskUsage, error)
	VolumeUsage(ctx context.Context) ([]VolumeUsage, error)
	PruneImages(ctx context.Context, opts PruneImagesOptions) (PruneReport, error)
	PruneBuildCache(ctx context.Context) (PruneReport, error)

	// StreamEvents streams Docker daemon events for Miabi-managed
	// containers until the context is cancelled or an error occurs.
	StreamEvents(ctx context.Context, sink func(EngineEvent) error) error

	// Exec starts an interactive command inside a running container and returns
	// a bidirectional stream to it (used for the in-panel shell). Callers must
	// Close the returned stream.
	Exec(ctx context.Context, containerID string, opts ExecOptions) (ExecStream, error)

	// Top lists the running processes in a container (the "docker top" view).
	// psArgs are ps flags (e.g. "aux"); blank uses the daemon default. Read-only.
	Top(ctx context.Context, containerID, psArgs string) (ProcessList, error)

	// Streaming. The sink is invoked per item; return a non-nil error to stop.
	StreamLogs(ctx context.Context, id string, follow bool, tail string, sink func(LogLine) error) error
	StreamStats(ctx context.Context, id string, sink func(StatsSample) error) error
	// StatsOnce returns a single resource-usage sample.
	StatsOnce(ctx context.Context, id string) (StatsSample, error)

	// Networks & volumes.
	EnsureNetwork(ctx context.Context, name string) (string, error)
	CreateNetwork(ctx context.Context, name, driver string, internal bool) (string, error)
	// CreateNetworkSpec creates a managed network with explicit options, including
	// an optional IPAM Subnet/Gateway (Miabi-allocated) so creation does not draw
	// from Docker's small built-in address pools. Always creates (errors if the
	// name exists); EnsureNetworkSpec is the create-or-reuse variant.
	CreateNetworkSpec(ctx context.Context, spec NetworkSpec) (string, error)
	EnsureNetworkSpec(ctx context.Context, spec NetworkSpec) (string, error)
	RemoveNetwork(ctx context.Context, name string) error
	ListNetworks(ctx context.Context) ([]Network, error)
	// NetworkConnect attaches a running container to a network with optional DNS
	// aliases; idempotent (no-op if already attached). NetworkDisconnect detaches
	// it; idempotent (no-op if not attached). Used to keep only route-exposed apps
	// on the shared reverse-proxy network.
	NetworkConnect(ctx context.Context, name, containerID string, aliases []string) error
	NetworkDisconnect(ctx context.Context, name, containerID string, force bool) error
	CreateVolume(ctx context.Context, name string, labels map[string]string, sizeBytes int64) (Volume, error)
	// CreateVolumeWith creates a managed volume with an explicit driver and driver
	// options (shared storage: nfs/cifs). An empty Driver uses the local default.
	CreateVolumeWith(ctx context.Context, spec VolumeSpec) (Volume, error)
	ListVolumes(ctx context.Context) ([]Volume, error)
	InspectVolume(ctx context.Context, name string) (Volume, error)
	RemoveVolume(ctx context.Context, name string, force bool) error

	// Swarm. Miabi drives Docker Swarm as an internal implementation detail of
	// cluster mode; users never run docker swarm/service themselves. Swarm reads
	// the engine's swarm state from `docker info` (works on any node); the rest
	// require a reachable manager and are no-ops on plain Docker.
	Swarm(ctx context.Context) (SwarmInfo, error)
	SwarmInit(ctx context.Context, req SwarmInitRequest) (nodeID string, err error)
	SwarmJoin(ctx context.Context, req SwarmJoinRequest) error
	SwarmLeave(ctx context.Context, force bool) error
	SwarmJoinTokens(ctx context.Context) (SwarmJoinTokens, error)
	SwarmNodes(ctx context.Context) ([]SwarmNode, error)
	SwarmNodeRemove(ctx context.Context, nodeID string, force bool) error

	// Swarm services (cluster apps). Require a reachable manager; cluster deploys
	// call these instead of RunContainer. CreateOverlayNetwork ensures the
	// per-workspace overlay an app's service attaches to.
	ServiceCreate(ctx context.Context, spec ServiceSpec) (id string, err error)
	ServiceUpdate(ctx context.Context, idOrName string, spec ServiceSpec) error
	ServiceRemove(ctx context.Context, idOrName string) error
	ServiceScale(ctx context.Context, idOrName string, replicas uint64) error
	ServiceInspect(ctx context.Context, idOrName string) (ServiceStatus, error)
	ServiceList(ctx context.Context) ([]ServiceStatus, error)
	// ServiceRestart forces a rolling restart of a service's tasks in place.
	ServiceRestart(ctx context.Context, idOrName string) error
	// ServiceTaskContainerID resolves a running task container of the named
	// service on this engine, for logs/stats/exec/top. ErrNotFound if none here.
	ServiceTaskContainerID(ctx context.Context, serviceName string) (string, error)
	// StreamServiceLogs streams a swarm service's logs from the MANAGER, aggregated
	// across every task wherever it was scheduled. This is the only way to read the
	// logs of a task on a swarm node Miabi has no Docker client for (an unmanaged
	// member with no agent) — the manager pulls them over the swarm control plane.
	StreamServiceLogs(ctx context.Context, serviceName string, follow bool, tail string, sink func(LogLine) error) error
	CreateOverlayNetwork(ctx context.Context, name string) (id string, err error)

	// Close releases the underlying connection.
	Close() error
}
