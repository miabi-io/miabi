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

// Package wsbundle defines the Miabi Portable Bundle format; it depends on nothing else in Miabi.
package wsbundle

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// Restore matches on the prefix, so a mistyped argument fails fast instead of listing a whole bucket.
const RefPrefix = "mbwb_"

// StateExt is the sealed state file's extension; PlainStateExt the unencrypted one's.
const (
	StateExt      = ".mbws"
	PlainStateExt = ".json"
)

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

// A per-bundle branch lets restore list one bundle's artifacts and an operator delete it by one prefix.
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

func StateObject(prefix, ref string, encrypted bool) string {
	return Root(prefix, ref) + "/" + StateFile(ref, encrypted)
}

// StateFile is the state file's name within the bundle's branch.
func StateFile(ref string, encrypted bool) string {
	if encrypted {
		return "state-" + ref + StateExt
	}
	return "state-" + ref + PlainStateExt
}

// The info file sits at the top of the prefix so listing it lists bundles rather than their contents.
func InfoObject(prefix, ref string) string {
	name := "workspace-" + ref + InfoExt
	if p := strings.Trim(strings.TrimSpace(prefix), "/"); p != "" {
		return path.Join(p, name)
	}
	return name
}

// Lets a restore discover bundles in a bucket with no database to list them from.
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
