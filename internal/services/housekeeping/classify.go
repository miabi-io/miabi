// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import (
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// The managed-resource label scheme lives in the docker package (io.miabi.*), and the owner a label names
// is resolved by the drift package. Housekeeping joins live Docker against the DB by these labels.

// Local aliases for the platform label keys (canonical definitions live in the
// docker package); used by the package tests.
const (
	labelApp       = docker.LabelApp
	labelDatabase  = docker.LabelDatabase
	labelStack     = docker.LabelStack
	labelVolume    = docker.LabelVolume
	labelRole      = docker.LabelRole
	labelJob       = docker.LabelJob
	labelWorkspace = docker.LabelWorkspace
)

// isManaged reports whether a resource carries any platform label (i.e. is
// owned by the platform). The safety contract: managed resources are never a
// blanket prune target — they are reclaimed only via the precise drift path.
func isManaged(labels map[string]string) bool {
	return docker.IsManaged(labels)
}

// isPlatformInfra reports whether a managed resource is platform infrastructure that housekeeping must
// never classify as an orphan or remove: the node's edge gateway / its Redis (io.miabi.role). These are
// managed through their own pages, not reclaimed here.
func isPlatformInfra(labels map[string]string) bool {
	return docker.IsPlatformInfra(labels)
}

// swarmServiceName returns the swarm service a container is a task of, or "" when it is a plain
// container. Swarm reconciles its own tasks, so this decides whether the reclaimable resource is
// the container or the service above it.
func swarmServiceName(labels map[string]string) string {
	return labels[docker.SwarmServiceNameLabel]
}

// isMiabiVolumeName reports whether name is the Docker name storage gives a volume,
// mb-vol-<workspaceID>-<name>. Keep in step with storage.CreateWith.
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
