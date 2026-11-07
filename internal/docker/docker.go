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

// Package docker is the sole adapter over the Docker SDK, keeping SDK types out of the rest of the codebase.
package docker

import (
	"context"
	"errors"
	"io"
	"net"

	stackdocker "github.com/miabi-io/miabi/pkg/stack/docker"
)

// ErrNotFound is owned by the stack seam so both sides compare against one value.
var ErrNotFound = stackdocker.ErrNotFound

// ErrEngineTooOld replaces the SDK's obscure error for daemons below MinAPIVersion.
var ErrEngineTooOld = errors.New("docker: engine too old")

const (
	// MinAPIVersion is the SDK's hard technical floor, distinct from the supported MinEngineVersion.
	MinAPIVersion = "1.40"
	// MinEngineVersion is the supported floor (CI matrix 25-29), quoted by the README, installer and Nodes badge.
	MinEngineVersion = "25.0"
)

// Client is the abstraction the rest of the app depends on.
type Client interface {
	// Ping verifies the daemon is reachable.
	Ping(ctx context.Context) error
	// Info returns engine information.
	Info(ctx context.Context) (Info, error)
	Capabilities(ctx context.Context) (Capabilities, error)

	ListContainers(ctx context.Context, all bool) ([]Container, error)
	InspectContainer(ctx context.Context, id string) (Container, error)
	InspectContainerConfig(ctx context.Context, id string) (ContainerConfig, error)
	RunContainer(ctx context.Context, spec RunSpec) (string, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeoutSeconds int) error
	RestartContainer(ctx context.Context, id string, timeoutSeconds int) error
	RemoveContainer(ctx context.Context, id string, force bool) error

	RunOneShot(ctx context.Context, spec RunSpec) (exitCode int, logs string, err error)

	RunOneShotStream(ctx context.Context, spec RunSpec, sink func(LogLine) error) (exitCode int, err error)

	ReadContainerFile(ctx context.Context, containerID, path string) ([]byte, error)

	// Closing the returned reader also removes the helper container.
	CopyFileFromVolume(ctx context.Context, volume, image, file string) (rc io.ReadCloser, size int64, err error)

	// image must already be present on the node.
	CopyToVolume(ctx context.Context, volume, image, name string, content io.Reader, size int64) error

	// DialNetwork uses an ephemeral socat relay over the engine connection, so it works tunneled without a host port.
	DialNetwork(ctx context.Context, network, image, host string, port int) (net.Conn, error)

	RunAttached(ctx context.Context, spec RunSpec) (Attached, error)

	// Images. auth may be nil for anonymous (public) pulls.
	PullImage(ctx context.Context, ref string, auth *RegistryAuth) error
	// TagImage adds target as an additional tag for the local source image.
	TagImage(ctx context.Context, source, target string) error
	// PushImage pushes a local image ref to its registry. auth may be nil.
	PushImage(ctx context.Context, ref string, auth *RegistryAuth) error
	BuildImage(ctx context.Context, contextDir, dockerfile, tag string, sink func(LogLine) error) error
	// InspectImage returns a local image's identity (ID + digest) and size.
	InspectImage(ctx context.Context, ref string) (ImageInspect, error)
	// ImageExists reports whether ref is present locally (no pull).
	ImageExists(ctx context.Context, ref string) (bool, error)
	RemoveImage(ctx context.Context, ref string, force bool) error

	// Prune* target only the safe set (dangling images, build cache); callers add their own managed-resource guards.
	ListImages(ctx context.Context) ([]Image, error)
	DiskUsage(ctx context.Context) (DiskUsage, error)
	VolumeUsage(ctx context.Context) ([]VolumeUsage, error)
	PruneImages(ctx context.Context, opts PruneImagesOptions) (PruneReport, error)
	PruneBuildCache(ctx context.Context) (PruneReport, error)

	StreamEvents(ctx context.Context, sink func(EngineEvent) error) error

	// Callers must Close the returned stream.
	Exec(ctx context.Context, containerID string, opts ExecOptions) (ExecStream, error)

	Top(ctx context.Context, containerID, psArgs string) (ProcessList, error)

	// The sink is invoked per item; return a non-nil error to stop.
	StreamLogs(ctx context.Context, id string, follow bool, tail string, sink func(LogLine) error) error
	StreamStats(ctx context.Context, id string, sink func(StatsSample) error) error
	// StatsOnce returns a single resource-usage sample.
	StatsOnce(ctx context.Context, id string) (StatsSample, error)

	EnsureNetwork(ctx context.Context, name string) (string, error)
	CreateNetwork(ctx context.Context, name, driver string, internal bool) (string, error)
	// CreateNetworkSpec errors if the name exists; EnsureNetworkSpec is the create-or-reuse variant.
	CreateNetworkSpec(ctx context.Context, spec NetworkSpec) (string, error)
	EnsureNetworkSpec(ctx context.Context, spec NetworkSpec) (string, error)
	RemoveNetwork(ctx context.Context, name string) error
	ListNetworks(ctx context.Context) ([]Network, error)
	// InspectNetwork returns ErrNotFound when the network does not exist.
	InspectNetwork(ctx context.Context, name string) (NetworkState, error)
	// Rootless reports whether the daemon runs in rootless mode.
	Rootless(ctx context.Context) (bool, error)
	// Both are idempotent; used to keep only route-exposed apps on the shared proxy network.
	NetworkConnect(ctx context.Context, name, containerID string, aliases []string) error
	NetworkDisconnect(ctx context.Context, name, containerID string, force bool) error
	CreateVolume(ctx context.Context, name string, labels map[string]string, sizeBytes int64) (Volume, error)
	CreateVolumeWith(ctx context.Context, spec VolumeSpec) (Volume, error)
	ListVolumes(ctx context.Context) ([]Volume, error)
	InspectVolume(ctx context.Context, name string) (Volume, error)
	RemoveVolume(ctx context.Context, name string, force bool) error

	// Users never run docker swarm/service themselves; all but Swarm need a reachable manager.
	Swarm(ctx context.Context) (SwarmInfo, error)
	SwarmInit(ctx context.Context, req SwarmInitRequest) (nodeID string, err error)
	SwarmJoin(ctx context.Context, req SwarmJoinRequest) error
	SwarmLeave(ctx context.Context, force bool) error
	SwarmJoinTokens(ctx context.Context) (SwarmJoinTokens, error)
	SwarmNodes(ctx context.Context) ([]SwarmNode, error)
	SwarmNodeRemove(ctx context.Context, nodeID string, force bool) error
	// Drain makes a node safe to reboot by rescheduling its tasks away.
	SwarmNodeAvailability(ctx context.Context, nodeID, availability string) error
	SwarmNodeSetLabel(ctx context.Context, nodeID, key, value string) error
	// Only the manager can list tasks: Miabi may hold no Docker client for the nodes.
	SwarmTasks(ctx context.Context, nodeID string) ([]SwarmTask, error)

	ServiceCreate(ctx context.Context, spec ServiceSpec) (id string, err error)
	ServiceUpdate(ctx context.Context, idOrName string, spec ServiceSpec) error
	ServiceRemove(ctx context.Context, idOrName string) error
	ServiceScale(ctx context.Context, idOrName string, replicas uint64) error
	ServiceSetEndpointMode(ctx context.Context, idOrName, mode string) error
	ServiceSetIngressNetwork(ctx context.Context, idOrName, network, alias string, attached bool) error
	ServiceInspect(ctx context.Context, idOrName string) (ServiceStatus, error)

	// Swarm config objects backing kind: Config on a replicated service.
	EnsureConfig(ctx context.Context, obj ConfigObject) (string, error)
	ListManagedConfigs(ctx context.Context) ([]ConfigInfo, error)
	RemoveConfig(ctx context.Context, id string) error
	ServiceList(ctx context.Context) ([]ServiceStatus, error)
	// ServiceRestart forces a rolling restart of a service's tasks in place.
	ServiceRestart(ctx context.Context, idOrName string) error
	ServiceTaskContainerID(ctx context.Context, serviceName string) (string, error)
	// ServiceEnv can carry secrets, so it is for the service layer to inspect, never to return to a client.
	ServiceEnv(ctx context.Context, idOrName string) ([]string, error)
	// StreamServiceLogs reads from the manager, the only way to reach tasks on nodes Miabi has no client for.
	StreamServiceLogs(ctx context.Context, serviceName string, follow bool, tail string, sink func(LogLine) error) error
	CreateOverlayNetwork(ctx context.Context, name string) (id string, err error)

	// Close releases the underlying connection.
	Close() error
}
