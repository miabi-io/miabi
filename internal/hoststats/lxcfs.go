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

package hoststats

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// lxcfs answers for the reader's cgroup, so inside a container it reports that container, not the host.
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
