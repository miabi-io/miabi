// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import (
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// The managed-resource label scheme lives in the docker package (io.miabi.*).
// Housekeeping joins live Docker against the DB by these labels to classify drift.

// Owner kinds — the DB record class a managed resource belongs to.
const (
	OwnerApp      = "app"
	OwnerDatabase = "database"
	OwnerVolume   = "volume"
	OwnerStack    = "stack"
)

// Local aliases for the platform label keys (canonical definitions live in the
// docker package); used here and by the package tests.
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

// ownerOf returns the owning DB record (kind and numeric id) encoded in a managed resource's labels, so drift
// can check whether that record still exists. ok is false when the resource is not orphan-eligible. Precedence
// matters: an app's container carries both app and stack labels, and the app is the owning record.
func ownerOf(labels map[string]string) (kind string, id uint, ok bool) {
	if isPlatformInfra(labels) {
		return "", 0, false
	}
	if _, isJob := docker.LabelValue(labels, docker.LabelJob); isJob {
		return "", 0, false // jobs are one-shot; their leftovers are not "deleted workloads"
	}
	if v, present := docker.LabelValue(labels, docker.LabelApp); present {
		id, ok = parseID(v)
		return OwnerApp, id, ok
	}
	if v, present := docker.LabelValue(labels, docker.LabelDatabase); present {
		id, ok = parseID(v)
		return OwnerDatabase, id, ok
	}
	if v, present := docker.LabelValue(labels, docker.LabelVolume); present {
		id, ok = parseID(v)
		return OwnerVolume, id, ok
	}
	if v, present := docker.LabelValue(labels, docker.LabelStack); present {
		id, ok = parseID(v)
		return OwnerStack, id, ok
	}
	return "", 0, false
}

func parseID(s string) (uint, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint(n), true
}
