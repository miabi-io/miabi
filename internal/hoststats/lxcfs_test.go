// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package hoststats

import (
	"strings"
	"testing"
)

// Taken from the control plane container on an LXC test VM, where /proc is bound to /host/proc.
const lxcMountinfo = `5691 5663 0:112 / /host/proc ro,nosuid,nodev,noexec,relatime - proc proc rw
5697 5691 0:44 /proc/cpuinfo /host/proc/cpuinfo ro,nosuid,nodev,relatime - fuse.lxcfs lxcfs rw,user_id=0,group_id=0,allow_other
5700 5691 0:44 /proc/meminfo /host/proc/meminfo ro,nosuid,nodev,relatime - fuse.lxcfs lxcfs rw,user_id=0,group_id=0,allow_other
5703 5691 0:44 /proc/stat /host/proc/stat ro,nosuid,nodev,relatime - fuse.lxcfs lxcfs rw,user_id=0,group_id=0,allow_other
`

const plainMountinfo = `5691 5663 0:112 / /host/proc ro,nosuid,nodev,noexec,relatime - proc proc rw
812 5691 0:44 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw
`

func TestLXCFSDetection(t *testing.T) {
	cases := []struct {
		name, info, proc string
		want             bool
	}{
		{"lxc guest, bound procfs", lxcMountinfo, "/host/proc", true},
		{"lxc guest, trailing slash", lxcMountinfo, "/host/proc/", true},
		{"lxc guest, other path", lxcMountinfo, "/proc", false},
		{"ordinary host", plainMountinfo, "/host/proc", false},
		{"garbage", "not mountinfo\n", "/host/proc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lxcfsMounted(strings.NewReader(tc.info), tc.proc); got != tc.want {
				t.Fatalf("lxcfsMounted = %v, want %v", got, tc.want)
			}
		})
	}
}
