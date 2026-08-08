// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wsbundle holds the format of a Miabi Portable Bundle: one workspace, its configuration,
// secrets and data, written to an S3 bucket as a self-describing set of objects. It depends on
// nothing else in Miabi — a bundle is meant to be read by an instance that did not write it.
package wsbundle

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// RefPrefix marks a workspace bundle. Restore matches on it, so a mistyped
// argument fails fast instead of listing a whole bucket.
const RefPrefix = "mbwb_"

// StateExt is the sealed state file's extension.
const StateExt = ".mbws"

// NewRef builds a bundle's stable name from the workspace it came from and the
// moment it started: "mbwb_shop_20260731T020000Z".
func NewRef(workspace string, at time.Time) string {
	ws := Segment(workspace)
	if ws == "" {
		ws = "workspace"
	}
	return fmt.Sprintf("%s%s_%s", RefPrefix, ws, at.UTC().Format("20060102T150405Z"))
}

// IsRef reports whether s looks like a bundle ref.
func IsRef(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), RefPrefix) }

// Segment reduces a name to something safe in an object key.
func Segment(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// Root is the object prefix every artifact of one bundle lives under: "<prefix>/<ref>". Its own
// branch is what lets a restore list one bundle's artifacts without reading every object in the
// bucket, and what lets an operator delete a bundle by removing a single prefix.
func Root(prefix, ref string) string {
	if p := strings.Trim(strings.TrimSpace(prefix), "/"); p != "" {
		return p + "/" + ref
	}
	return ref
}

// DatabasePath is where a bundle's database dumps live.
func DatabasePath(prefix, ref string) string { return Root(prefix, ref) + "/databases" }

// VolumePath is where a bundle's volume archives live.
func VolumePath(prefix, ref string) string { return Root(prefix, ref) + "/volumes" }

// StateObject is the sealed state file's object key. It sits inside the bundle's
// own branch, next to the data it describes.
func StateObject(prefix, ref string) string {
	return Root(prefix, ref) + "/state-" + ref + StateExt
}

// InfoObject is the info file's object key. Unlike everything else it sits at the TOP of the
// configured prefix, not inside the bundle's branch: it is the one file a restore is expected to
// find by looking, so listing the prefix lists the bundles rather than their contents.
func InfoObject(prefix, ref string) string {
	name := "workspace-" + ref + InfoExt
	if p := strings.Trim(strings.TrimSpace(prefix), "/"); p != "" {
		return path.Join(p, name)
	}
	return name
}

// RefFromInfoObject recovers the ref from an info file's object key, or "".
// It is what lets a restore discover the bundles in a bucket with no database to
// list them from.
func RefFromInfoObject(key string) string {
	base := path.Base(strings.TrimSpace(key))
	if !strings.HasPrefix(base, "workspace-") || !strings.HasSuffix(base, InfoExt) {
		return ""
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(base, "workspace-"), InfoExt)
	if !IsRef(ref) {
		return ""
	}
	return ref
}
