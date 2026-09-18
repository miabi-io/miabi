// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import "testing"

// A NoCreate bind must NOT become a bind string: the daemon silently creates a missing bind-string
// source, which would turn an unmounted disk into an empty directory on the root filesystem.
func TestNoCreateBindUsesTheMountAPI(t *testing.T) {
	mounts := []BindMount{
		{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		{Source: "/mnt/ssd1/miabi", Target: "/mnt/class", NoCreate: true},
	}

	binds := hostBinds(mounts)
	if len(binds) != 1 || binds[0] != "/var/run/docker.sock:/var/run/docker.sock" {
		t.Fatalf("bind strings = %v, want only the auto-create bind", binds)
	}

	noCreate := hostBindMounts(mounts)
	if len(noCreate) != 1 {
		t.Fatalf("mount entries = %d, want 1", len(noCreate))
	}
	m := noCreate[0]
	if m.Source != "/mnt/ssd1/miabi" || m.Target != "/mnt/class" {
		t.Fatalf("mount = %+v", m)
	}
	if m.BindOptions == nil || m.BindOptions.CreateMountpoint {
		t.Fatal("CreateMountpoint must stay false so a missing path is refused")
	}
}
