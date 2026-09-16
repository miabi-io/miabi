// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"context"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// ContainerNameOf returns a container's primary name without Docker's leading slash, or "" when it has none.
func ContainerNameOf(c docker.Container) string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// FindCentral returns the platform's own gateway on the node: the Goma container the Miabi stack installs
// and owns, which serves every app of the default cluster.
//
// It is matched by role label first — a compose project prefix or a rename cannot hide it that way — and by
// the conventional container name second, for stacks installed before the label existed. Miabi never
// recreates this container: the stack owns its spec. Finding it is what lets Miabi adopt it as the manager's
// gateway instead of asking an operator to import it by hand after every fresh install.
func FindCentral(ctx context.Context, dc docker.Client) (docker.Container, bool) {
	list, err := dc.ListContainers(ctx, true)
	if err != nil {
		return docker.Container{}, false
	}
	var byName docker.Container
	foundByName := false
	for _, c := range list {
		if role, ok := docker.LabelValue(c.Labels, docker.LabelRole); ok && role == docker.RoleGateway {
			return c, true
		}
		if ContainerNameOf(c) == CentralContainerName {
			byName, foundByName = c, true
		}
	}
	return byName, foundByName
}
