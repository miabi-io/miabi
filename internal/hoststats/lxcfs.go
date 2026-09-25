// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package hoststats

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ServedByLXCFS reports whether procPath's stat or meminfo is an lxcfs mount, as on an LXC guest.
// lxcfs answers for the cgroup of the process reading the file, so from inside a container it
// reports that container's own usage rather than the host's.
func ServedByLXCFS(procPath string) bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return lxcfsMounted(f, procPath)
}

func lxcfsMounted(mountinfo io.Reader, procPath string) bool {
	want := map[string]bool{
		filepath.Join(procPath, "stat"):    true,
		filepath.Join(procPath, "meminfo"): true,
	}
	sc := bufio.NewScanner(mountinfo)
	for sc.Scan() {
		// "<id> <parent> <dev> <root> <mount point> <options> [optional...] - <fstype> <source> <super options>"
		pre, post, ok := strings.Cut(sc.Text(), " - ")
		if !ok {
			continue
		}
		fields := strings.Fields(pre)
		fs := strings.Fields(post)
		if len(fields) < 5 || len(fs) == 0 {
			continue
		}
		if want[fields[4]] && fs[0] == "fuse.lxcfs" {
			return true
		}
	}
	return false
}
