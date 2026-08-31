// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

// Package selfcontainer detects the Docker container ID of the running process,
// so Miabi can recognise its own container and refuse to stop or delete it from
// the admin containers list — which would take the platform offline.
package selfcontainer

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// containerID is a 64-hex Docker/containerd container identifier.
var containerID = regexp.MustCompile(`[0-9a-f]{64}`)

// Detect returns the ID of the container the process runs in, or "" when it is not containerized.
// Resolution order, most to least reliable: MIABI_CONTAINER_ID, /proc/self/mountinfo (Docker
// bind-mounts /etc/hostname from /var/lib/docker/containers/<id>/), /proc/self/cgroup, hostname.
func Detect() string {
	if v := strings.TrimSpace(os.Getenv("MIABI_CONTAINER_ID")); v != "" {
		return v
	}
	// Only the /containers/<id>/ bind-mount paths name the container itself;
	// overlay layer dirs are also 64-hex, so the filter must be specific.
	if id := scan("/proc/self/mountinfo", "/containers/"); id != "" {
		return id
	}
	if id := scan("/proc/self/cgroup", ""); id != "" {
		return id
	}
	if h, err := os.Hostname(); err == nil {
		h = strings.TrimSpace(h)
		if len(h) == 12 && isHex(h) {
			return h
		}
	}
	return ""
}

// scan returns the first 64-hex container ID found on a line of path. When must
// is non-empty, only lines containing that substring are considered.
func scan(path, must string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if must != "" && !strings.Contains(line, must) {
			continue
		}
		if id := containerID.FindString(line); id != "" {
			return id
		}
	}
	return ""
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Match reports whether two container references identify the same container, tolerating the mix
// of short (12-char) and full (64-char) IDs Docker returns. It requires at least a 12-char overlap,
// so an empty reference never matches.
func Match(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 12 && strings.HasPrefix(b, a)
}
