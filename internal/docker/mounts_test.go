// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Under the restricted profile (NoCopyVolumes) a named volume must use the Mount
// API with NoCopy set — otherwise Docker re-applies the image dir's ownership and
// undoes the prep chown, leaving the non-root process unable to write.
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

// A volume named in ReadOnlyMounts must render read-only, so a backup helper cannot write to the
// data it archives. Both rendering paths matter: bind strings normally, and the Mount API when
// copy-up is disabled.
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
