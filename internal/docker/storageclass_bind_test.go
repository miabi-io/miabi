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

package docker

import "testing"

// The daemon silently creates a missing bind-string source, turning an unmounted disk into a root-fs directory.
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
