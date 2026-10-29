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
	// InspectNetwork returns ErrNotFound when the network does not exist.
	InspectNetwork(ctx context.Context, name string) (NetworkState, error)
	RemoveNetwork(ctx context.Context, name string) error

	// Rootless reports whether the daemon runs in rootless mode, which the stack does not support.
	Rootless(ctx context.Context) (bool, error)

	CreateVolume(ctx context.Context, name string, labels map[string]string, sizeBytes int64) (Volume, error)
	InspectVolume(ctx context.Context, name string) (Volume, error)
	RemoveVolume(ctx context.Context, name string, force bool) error
}

// ErrNotFound reports that a container, image, network or volume does not exist. Callers compare
// against it with errors.Is rather than matching on the engine's message text.
var ErrNotFound = errors.New("docker: resource not found")
