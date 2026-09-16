// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package drift is the shared vocabulary for divergence between what Miabi recorded and what Docker
// runs. Node housekeeping reports it for one node; the control manager watches it across the platform.
package drift

import (
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// Classes.
const (
	ClassOrphan    = "orphan"    // managed label present, DB record gone — still running on the node
	ClassMissing   = "missing"   // DB record expects it live, nothing running for it
	ClassUntracked = "untracked" // no miabi.* label — a hand-run resource
	// ClassReplaced is a resource whose record is intact but whose contents are not: a volume deleted and
	// recreated by hand keeps its name and its row, and holds none of the data they describe.
	ClassReplaced = "replaced"
)

// Recommended actions per item.
const (
	ActionRemove   = "remove"   // orphan → delete the lingering resource
	ActionRedeploy = "redeploy" // missing → redeploy from the owning resource
	ActionImport   = "import"   // untracked → adopt via the existing import flow
	// ActionRestore is for lost data: recreating the volume would give an empty one, so the way back is a
	// backup, not a redeploy.
	ActionRestore = "restore"
)

// Owner kinds: the DB record class a managed resource belongs to.
const (
	OwnerApp      = "app"
	OwnerDatabase = "database"
	OwnerVolume   = "volume"
	OwnerStack    = "stack"
	OwnerConfig   = "config"
)

// Item is one resource that diverges from intent.
type Item struct {
	Class     string `json:"class"`
	Kind      string `json:"kind"` // container | volume | config | service
	Ref       string `json:"ref"`  // container ID or volume name (the apply handle)
	Name      string `json:"name"`
	Image     string `json:"image,omitempty"`
	State     string `json:"state,omitempty"`
	OwnerKind string `json:"owner_kind,omitempty"` // app | database | volume | stack
	OwnerID   uint   `json:"owner_id,omitempty"`
	Action    string `json:"action"`
}

// OwnerOf returns the owning DB record (kind and numeric id) encoded in a managed resource's labels, so drift
// can check whether that record still exists. ok is false when the resource is not orphan-eligible. Precedence
// matters: an app's container carries both app and stack labels, and the app is the owning record.
func OwnerOf(labels map[string]string) (kind string, id uint, ok bool) {
	if docker.IsPlatformInfra(labels) {
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

// VolumeOwner returns the record a managed Docker volume backs: a volume row, or the database instance
// whose data it holds. App and stack labels are not owners here: neither record owns a volume's data.
func VolumeOwner(labels map[string]string) (kind string, id uint, ok bool) {
	if docker.IsPlatformInfra(labels) {
		return "", 0, false
	}
	if v, present := docker.LabelValue(labels, docker.LabelVolume); present {
		id, ok = parseID(v)
		return OwnerVolume, id, ok
	}
	if v, present := docker.LabelValue(labels, docker.LabelDatabase); present {
		id, ok = parseID(v)
		return OwnerDatabase, id, ok
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
