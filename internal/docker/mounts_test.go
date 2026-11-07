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

import (
	"slices"
	"testing"
)

// By default named volumes are plain bind strings (Docker copy-up seeds them).
func TestContainerVolumeMountsDefault(t *testing.T) {
	binds, mounts := containerVolumeMounts(RunSpec{
		Mounts: map[string]string{"mb-vol-7": "/data"},
	})
	if len(mounts) != 0 {
		t.Fatalf("want no Mount-API mounts by default, got %d", len(mounts))
	}
	if len(binds) != 1 || binds[0] != "mb-vol-7:/data" {
		t.Fatalf("binds = %v, want [mb-vol-7:/data]", binds)
	}
}

// Without NoCopy Docker re-applies the image dir's ownership and undoes the prep chown.
func TestContainerVolumeMountsNoCopy(t *testing.T) {
	binds, mounts := containerVolumeMounts(RunSpec{
		Mounts:        map[string]string{"mb-vol-7": "/data"},
		NoCopyVolumes: true,
		Binds:         []BindMount{{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"}},
	})
	if len(mounts) != 1 {
		t.Fatalf("want 1 Mount-API mount, got %d", len(mounts))
	}
	m := mounts[0]
	if m.Source != "mb-vol-7" || m.Target != "/data" {
		t.Errorf("mount = %+v, want source mb-vol-7 target /data", m)
	}
	if m.VolumeOptions == nil || !m.VolumeOptions.NoCopy {
		t.Errorf("NoCopy not set on the volume mount: %+v", m.VolumeOptions)
	}
	// The named volume must NOT also appear as a bind; host binds still do.
	if len(binds) != 1 || binds[0] != "/var/run/docker.sock:/var/run/docker.sock" {
		t.Errorf("binds = %v, want only the host bind", binds)
	}
}

// ReadOnlyMounts must render read-only on both paths so a backup helper cannot write to the data it archives.
func TestReadOnlyMountsRenderReadOnly(t *testing.T) {
	spec := RunSpec{Mounts: map[string]string{"mb-vol-1-data": "/data", "scratch": "/tmp/work"}, ReadOnlyMounts: []string{"mb-vol-1-data"}}

	binds, _ := containerVolumeMounts(spec)
	if !slices.Contains(binds, "mb-vol-1-data:/data:ro") {
		t.Errorf("bind strings = %v, want the archived volume mounted :ro", binds)
	}
	if !slices.Contains(binds, "scratch:/tmp/work") {
		t.Errorf("bind strings = %v, want the unlisted volume left writable", binds)
	}

	spec.NoCopyVolumes = true
	_, mounts := containerVolumeMounts(spec)
	for _, m := range mounts {
		switch m.Source {
		case "mb-vol-1-data":
			if !m.ReadOnly {
				t.Error("the archived volume must be read-only on the Mount API path too")
			}
		case "scratch":
			if m.ReadOnly {
				t.Error("an unlisted volume must stay writable")
			}
		}
	}
}
