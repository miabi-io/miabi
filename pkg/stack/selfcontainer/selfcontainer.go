// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

// Package selfcontainer detects Miabi's own container ID so the admin UI can refuse to stop or delete it.
package selfcontainer

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// containerID is a 64-hex Docker/containerd container identifier.
var containerID = regexp.MustCompile(`[0-9a-f]{64}`)

// Order: MIABI_CONTAINER_ID, /proc/self/mountinfo, /proc/self/cgroup, hostname.
func Detect() string {
	if v := strings.TrimSpace(os.Getenv("MIABI_CONTAINER_ID")); v != "" {
		return v
	}
	// Overlay layer dirs are also 64-hex, so only /containers/<id>/ bind-mount paths count.
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

func scan(path, must string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if id := idOn(sc.Text(), must); id != "" {
			return id
		}
	}
	return ""
}

// Under Docker in Docker, a volume's 64-hex name can appear earlier on the line.
func idOn(line, must string) string {
	if must == "" {
		return containerID.FindString(line)
	}
	for rest := line; ; {
		i := strings.Index(rest, must)
		if i < 0 {
			return ""
		}
		rest = rest[i+len(must):]
		if loc := containerID.FindStringIndex(rest); loc != nil && loc[0] == 0 {
			return rest[:loc[1]]
		}
	}
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Tolerates short and full IDs but requires a 12-char overlap, so an empty reference never matches.
func Match(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 12 && strings.HasPrefix(b, a)
}
