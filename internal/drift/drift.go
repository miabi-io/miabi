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

// Package drift is the shared vocabulary for divergence between Miabi's records and what Docker runs.
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
	// ClassReplaced is a hand-recreated volume: same name and row, none of the data.
	ClassReplaced = "replaced"
)

// Recommended actions per item.
const (
	ActionRemove   = "remove"   // orphan → delete the lingering resource
	ActionRedeploy = "redeploy" // missing → redeploy from the owning resource
	ActionImport   = "import"   // untracked → adopt via the existing import flow
	// ActionRestore: recreating the volume would give an empty one, so the way back is a backup.
	ActionRestore = "restore"
	// ActionNone is drift Miabi reports but may not touch, such as the stack-owned platform gateway.
	ActionNone = "none"
)

// Owner kinds: the DB record class a managed resource belongs to.
const (
	OwnerApp      = "app"
	OwnerDatabase = "database"
	OwnerVolume   = "volume"
	OwnerStack    = "stack"
	OwnerConfig   = "config"
	// OwnerNode is for what a node itself owns rather than a workspace: its gateway.
	OwnerNode = "node"
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

// OwnerOf gives the app precedence over the stack, since an app container carries both labels.
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

// VolumeOwner ignores app and stack labels: neither record owns a volume's data.
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
