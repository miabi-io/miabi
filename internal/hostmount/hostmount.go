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

// Package hostmount defines the allow-listed host bind mounts; sources are server-owned, never client-supplied.
package hostmount

import (
	"errors"
	"path"
	"sort"
	"strings"
)

// Narrow on purpose: binds under /mnt can't reach system paths and stay useful for shared storage.
const CustomHostRoot = "/mnt"

var ErrInvalidHostPath = errors.New("host path must be an absolute path under " + CustomHostRoot + "/ (e.g. " + CustomHostRoot + "/nas/app)")

// The source-under-/mnt rule is the trust boundary and can never widen.
func ValidateCustomHostPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsRune(p, 0) || !strings.HasPrefix(p, "/") {
		return "", ErrInvalidHostPath
	}
	clean := path.Clean(p)
	// Must be a strict subpath of /mnt (rules out "/mnt", "/mnt/..", "/mntx", "/").
	if clean == CustomHostRoot || !strings.HasPrefix(clean, CustomHostRoot+"/") {
		return "", ErrInvalidHostPath
	}
	return clean, nil
}

// Callers never accept a raw source from the client.
type Preset struct {
	Key             string `json:"key"`
	Label           string `json:"label"`
	Description     string `json:"description"`
	Source          string `json:"source"`            // host path bound into the container
	DefaultTarget   string `json:"default_target"`    // container path used when none is given
	DefaultReadOnly bool   `json:"default_read_only"` // UI default for the read-only toggle
	AllowReadOnly   bool   `json:"allow_read_only"`   // whether read-only is meaningful/toggleable
	Danger          string `json:"danger"`            // warning shown to the operator
}

// Adding a capability is a deliberate, reviewed change here; nothing else can be bound.
var presets = map[string]Preset{
	"docker-socket": {
		Key:             "docker-socket",
		Label:           "Docker socket",
		Description:     "Bind the host Docker socket so the app can control Docker on this node.",
		Source:          "/var/run/docker.sock",
		DefaultTarget:   "/var/run/docker.sock",
		DefaultReadOnly: false,
		AllowReadOnly:   false, // read-only on a socket is meaningless; the daemon protocol ignores it.
		Danger:          "Grants root-equivalent control of this node and every container on it.",
	},
	"docker-volumes": {
		Key:             "docker-volumes",
		Label:           "Docker volumes directory",
		Description:     "Bind the host Docker volumes directory (e.g. for backup or inspection tooling).",
		Source:          "/var/lib/docker/volumes",
		DefaultTarget:   "/var/lib/docker/volumes",
		DefaultReadOnly: true,
		AllowReadOnly:   true,
		Danger:          "Exposes the on-disk data of every Docker volume on this node.",
	},
	"host-proc": {
		Key:             "host-proc",
		Label:           "Host /proc",
		Description:     "Bind the host /proc filesystem (read-only) for monitoring agents that read host process and kernel metrics.",
		Source:          "/proc",
		DefaultTarget:   "/host/proc",
		DefaultReadOnly: true,
		AllowReadOnly:   true,
		Danger:          "Exposes every host process, its command line, and kernel/system metrics to the container.",
	},
}

// Get returns the preset for a key and whether it exists.
func Get(key string) (Preset, bool) {
	p, ok := presets[key]
	return p, ok
}

// All returns every preset, ordered by key (for catalog endpoints and the UI).
func All() []Preset {
	out := make([]Preset, 0, len(presets))
	for _, p := range presets {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
