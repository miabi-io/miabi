// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package stack

import (
	"os"
	"strconv"
	"syscall"
)

// dockerGID is the group that owns the Docker socket, so the control plane can talk
// to it without running as root. Best-effort: an empty result just means the
// container runs as its image default (root), which still works.
func dockerGID() string {
	fi, err := os.Stat("/var/run/docker.sock")
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return strconv.FormatUint(uint64(st.Gid), 10)
}
