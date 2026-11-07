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

package apply

import (
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
)

// Mounted content is invisible to every other diffed field.
func TestConfigFingerprintTracksContent(t *testing.T) {
	mounts := []declarative.MountSpec{{Config: "conf", Path: "/etc/app.conf"}}
	before := configFingerprint(mounts, map[string]string{"conf": "digest-one"})
	after := configFingerprint(mounts, map[string]string{"conf": "digest-two"})
	if before == "" || before == after {
		t.Fatalf("fingerprint did not follow the config digest: %q vs %q", before, after)
	}
}

// Mount order is not semantic, so reordering must not read as drift.
func TestConfigFingerprintIsOrderIndependent(t *testing.T) {
	digests := map[string]string{"a": "d1", "b": "d2"}
	one := configFingerprint([]declarative.MountSpec{
		{Config: "a", Path: "/etc/a"}, {Config: "b", Path: "/etc/b"},
	}, digests)
	two := configFingerprint([]declarative.MountSpec{
		{Config: "b", Path: "/etc/b"}, {Config: "a", Path: "/etc/a"},
	}, digests)
	if one != two {
		t.Fatalf("fingerprint depends on mount order: %q vs %q", one, two)
	}
}

// The mount path is part of the identity: moving a file is a real change.
func TestConfigFingerprintTracksPath(t *testing.T) {
	digests := map[string]string{"conf": "d1"}
	a := configFingerprint([]declarative.MountSpec{{Config: "conf", Path: "/etc/a.conf"}}, digests)
	b := configFingerprint([]declarative.MountSpec{{Config: "conf", Path: "/etc/b.conf"}}, digests)
	if a == b {
		t.Fatal("fingerprint ignores the mount path")
	}
}

// Otherwise every config-less app would show permanent drift.
func TestConfigFingerprintEmptyWithoutConfigMounts(t *testing.T) {
	got := configFingerprint([]declarative.MountSpec{{Volume: "data", Path: "/data"}}, map[string]string{})
	if got != "" {
		t.Fatalf("expected an empty fingerprint, got %q", got)
	}
}

// reloadPolicy: none suppresses the redeploy by leaving the fingerprint unset.
func TestStampConfigFPHonoursReloadPolicy(t *testing.T) {
	set := declarative.NewResourceSet()
	set.Add(declarative.Resource{
		APIVersion: declarative.APIVersion, Kind: declarative.KindConfig,
		Metadata: declarative.Meta{Name: "conf"},
		Config:   &declarative.ConfigSpec{Data: map[string]string{"a": "b"}, DigestFP: "digest-one"},
	})
	set.Add(declarative.Resource{
		APIVersion: declarative.APIVersion, Kind: declarative.KindApplication,
		Metadata: declarative.Meta{Name: "watcher"},
		Application: &declarative.ApplicationSpec{
			Image: "prom/prometheus", ReloadPolicy: declarative.ReloadNone,
			Mounts: []declarative.MountSpec{{Config: "conf", Path: "/etc/p.yml", ReadOnly: true}},
		},
	})
	set.Add(declarative.Resource{
		APIVersion: declarative.APIVersion, Kind: declarative.KindApplication,
		Metadata: declarative.Meta{Name: "restarter"},
		Application: &declarative.ApplicationSpec{
			Image:  "nginx",
			Mounts: []declarative.MountSpec{{Config: "conf", Path: "/etc/n.conf", ReadOnly: true}},
		},
	})

	(&Service{}).stampConfigFP(0, set)

	watcher, _ := set.Get(string(declarative.KindApplication) + "/watcher")
	if watcher.Application.ConfigFP != "" {
		t.Errorf("reloadPolicy: none should leave the fingerprint empty, got %q", watcher.Application.ConfigFP)
	}
	restarter, _ := set.Get(string(declarative.KindApplication) + "/restarter")
	if restarter.Application.ConfigFP == "" {
		t.Error("a restarting app should carry a fingerprint")
	}
}
