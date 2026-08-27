// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"errors"
)

// Client is the subset of a Docker engine the stack engine drives — 15 of the 68 methods the
// server's own client exposes. It is deliberately narrow: the CLI backs it with a moby/moby
// implementation, the control plane satisfies it structurally with the client it already has,
// and neither side's Docker SDK ever appears in a signature.
type Client interface {
	ListContainers(ctx context.Context, all bool) ([]Container, error)
	InspectContainer(ctx context.Context, id string) (Container, error)
	InspectContainerConfig(ctx context.Context, id string) (ContainerConfig, error)
	RunContainer(ctx context.Context, spec RunSpec) (string, error)
	RestartContainer(ctx context.Context, id string, timeoutSeconds int) error
	RemoveContainer(ctx context.Context, id string, force bool) error
	RunOneShot(ctx context.Context, spec RunSpec) (exitCode int, logs string, err error)

	PullImage(ctx context.Context, ref string, auth *RegistryAuth) error
	ImageExists(ctx context.Context, ref string) (bool, error)

	EnsureNetworkSpec(ctx context.Context, spec NetworkSpec) (string, error)
	// NetworkConnect / NetworkDisconnect move a RUNNING container between networks. The stack engine
	// otherwise recreates a component to change its spec, but a network split cannot be done that way:
	// between recreates the stack would be half on each network and the control plane would lose its
	// database. Attaching live first makes any recreate order safe.
	NetworkConnect(ctx context.Context, name, containerID string, aliases []string) error
	NetworkDisconnect(ctx context.Context, name, containerID string, force bool) error

	CreateVolume(ctx context.Context, name string, labels map[string]string, sizeBytes int64) (Volume, error)
	InspectVolume(ctx context.Context, name string) (Volume, error)
	RemoveVolume(ctx context.Context, name string, force bool) error
}

// ErrNotFound reports that a container, image, network or volume does not exist. Callers compare
// against it with errors.Is rather than matching on the engine's message text.
var ErrNotFound = errors.New("docker: resource not found")
