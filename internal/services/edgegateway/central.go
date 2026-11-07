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

// FindCentral matches by role label first, then by conventional name for stacks predating the label.
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
