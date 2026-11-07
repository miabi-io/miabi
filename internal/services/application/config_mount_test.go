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

package application

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// This shape is what makes the mount identity a (config, key) pair.
func mounts() []models.AppMount {
	return []models.AppMount{
		{VolumeID: 1, Path: "/data"},
		{ConfigID: 7, ConfigKey: "", Path: "/etc/app"},
		{ConfigID: 7, ConfigKey: "app.conf", Path: "/etc/app/app.conf"},
		{ConfigID: 7, ConfigKey: "log.conf", Path: "/etc/app/log.conf"},
	}
}

// Matching on config id alone would remove every file the config projects.
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

// Volumes and configs share one slice, so a careless filter drops storage too.
func TestRemoveConfigMountLeavesVolumes(t *testing.T) {
	out, _ := removeConfigMount(mounts(), 7, "")
	for _, m := range out {
		if m.VolumeID == 1 {
			return
		}
	}
	t.Fatal("the volume mount was removed")
}

// Not-found lets the API answer 404 instead of pretending to detach.
func TestRemoveConfigMountMissing(t *testing.T) {
	if _, removed := removeConfigMount(mounts(), 7, "absent.conf"); removed {
		t.Fatal("an absent mount must not report as removed")
	}
	if _, removed := removeConfigMount(mounts(), 99, ""); removed {
		t.Fatal("an unrelated config must not report as removed")
	}
}
