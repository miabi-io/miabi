// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hostmount defines the fixed, allow-listed set of host bind mounts a privileged workspace
// may attach to a container. The host source path is owned by the server and resolved from a preset
// key — never supplied by the client — so the blast radius can never widen beyond these presets.
package hostmount

import (
	"errors"
	"path"
	"sort"
	"strings"
)

// CustomHostRoot is the only host tree a privileged workspace may bind a custom path under.
// Deliberately narrow: /mnt is the conventional mount point for operator-managed external storage,
// so a bind under it can't reach system paths and stays useful for shared storage.
const CustomHostRoot = "/mnt"

// ErrInvalidHostPath is returned when a custom host path is not a clean absolute
// path strictly under CustomHostRoot.
var ErrInvalidHostPath = errors.New("host path must be an absolute path under " + CustomHostRoot + "/ (e.g. " + CustomHostRoot + "/nas/app)")

// ValidateCustomHostPath cleans p and verifies it is an absolute path strictly inside
// CustomHostRoot (a real subpath, not the root itself), with no traversal, returning the cleaned
// path. The source-under-/mnt rule is the trust boundary and can never widen.
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

// Preset is one allow-listed host bind. Source is the host path (server-owned);
// callers never accept a raw source from the client.
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

// presets is the complete allow-list. Adding a capability is a deliberate,
// reviewed change here — there is no path to bind anything else.
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
