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

package housekeeping

import (
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// Housekeeping joins live Docker against the DB by the io.miabi.* labels defined in the docker package.

const (
	labelApp       = docker.LabelApp
	labelDatabase  = docker.LabelDatabase
	labelStack     = docker.LabelStack
	labelVolume    = docker.LabelVolume
	labelRole      = docker.LabelRole
	labelJob       = docker.LabelJob
	labelWorkspace = docker.LabelWorkspace
)

// Managed resources are never a blanket prune target, only reclaimed via the precise drift path.
func isManaged(labels map[string]string) bool {
	return docker.IsManaged(labels)
}

// Edge gateways and their Redis are managed through their own pages, never classified as orphans.
func isPlatformInfra(labels map[string]string) bool {
	return docker.IsPlatformInfra(labels)
}

// Swarm reconciles its own tasks, so this decides whether the reclaimable resource is the container or service.
func swarmServiceName(labels map[string]string) string {
	return labels[docker.SwarmServiceNameLabel]
}

// Keep in step with storage.CreateWith.
func isMiabiVolumeName(name string) bool {
	rest, ok := strings.CutPrefix(name, "mb-vol-")
	if !ok {
		return false
	}
	ws, handle, ok := strings.Cut(rest, "-")
	if !ok || handle == "" {
		return false
	}
	_, ok = parseID(ws)
	return ok
}

func parseID(s string) (uint, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint(n), true
}
