// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// mounts is an app mounting one volume and the same config three ways: whole, and
// two individual files. That shape is what makes the mount identity a pair.
func mounts() []models.AppMount {
	return []models.AppMount{
		{VolumeID: 1, Path: "/data"},
		{ConfigID: 7, ConfigKey: "", Path: "/etc/app"},
		{ConfigID: 7, ConfigKey: "app.conf", Path: "/etc/app/app.conf"},
		{ConfigID: 7, ConfigKey: "log.conf", Path: "/etc/app/log.conf"},
	}
}

// TestRemoveConfigMountMatchesOnKeyToo is the case worth pinning: matching on the
// config id alone would remove every file the config projects, not the one asked for.
func TestRemoveConfigMountMatchesOnKeyToo(t *testing.T) {
	out, removed := removeConfigMount(mounts(), 7, "app.conf")
	if !removed {
		t.Fatal("the mount should have been found")
	}
	if len(out) != 3 {
		t.Fatalf("removed %d mounts, want exactly 1", 4-len(out))
	}
	for _, m := range out {
		if m.ConfigID == 7 && m.ConfigKey == "app.conf" {
			t.Fatal("the named mount survived")
		}
	}
}

// TestRemoveConfigMountWholeConfig removes the whole-config projection and leaves the
// per-file mounts of the same config alone.
func TestRemoveConfigMountWholeConfig(t *testing.T) {
	out, removed := removeConfigMount(mounts(), 7, "")
	if !removed || len(out) != 3 {
		t.Fatalf("removed=%v remaining=%d, want true/3", removed, len(out))
	}
	keys := map[string]bool{}
	for _, m := range out {
		if m.ConfigID == 7 {
			keys[m.ConfigKey] = true
		}
	}
	if !keys["app.conf"] || !keys["log.conf"] {
		t.Fatalf("per-file mounts were removed too: %v", keys)
	}
}

// TestRemoveConfigMountLeavesVolumes keeps the volume mount untouched — the two share
// one slice, so a careless filter drops storage along with configuration.
func TestRemoveConfigMountLeavesVolumes(t *testing.T) {
	out, _ := removeConfigMount(mounts(), 7, "")
	for _, m := range out {
		if m.VolumeID == 1 {
			return
		}
	}
	t.Fatal("the volume mount was removed")
}

// TestRemoveConfigMountMissing reports not-found rather than silently succeeding, so
// the API can answer 404 instead of pretending it detached something.
func TestRemoveConfigMountMissing(t *testing.T) {
	if _, removed := removeConfigMount(mounts(), 7, "absent.conf"); removed {
		t.Fatal("an absent mount must not report as removed")
	}
	if _, removed := removeConfigMount(mounts(), 99, ""); removed {
		t.Fatal("an unrelated config must not report as removed")
	}
}
