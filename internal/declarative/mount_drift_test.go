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

package declarative

import (
	"slices"
	"testing"
)

func appRes(mounts ...MountSpec) Resource {
	return Resource{APIVersion: APIVersion, Kind: KindApplication, Metadata: Meta{Name: "web"},
		Application: &ApplicationSpec{Image: "nginx", Tag: "1.27", Mounts: mounts}}
}

// An app created before its volume existed has no mount; the next apply must attach it, not report in sync.
func TestMissingMountIsDrift(t *testing.T) {
	desired := appRes(MountSpec{Volume: "data", Path: "/data"})
	if got := fieldNames(diffFields(appRes(), desired)); !slices.Contains(got, "mounts") {
		t.Fatalf("missing mount not detected, diff = %v", got)
	}
	moved := appRes(MountSpec{Volume: "data", Path: "/srv"})
	if got := fieldNames(diffFields(moved, desired)); !slices.Contains(got, "mounts") {
		t.Fatalf("changed mount path not detected, diff = %v", got)
	}
}

func TestEquivalentMountsAreNotDrift(t *testing.T) {
	live := appRes(
		MountSpec{Config: "conf", Key: "nginx.conf", Path: "/etc/nginx/nginx.conf", Mode: "0444", ReadOnly: true},
		MountSpec{Volume: "data", Path: "/data"},
	)
	desired := appRes(
		MountSpec{Volume: "data", Path: "/data", ReadOnly: true},
		MountSpec{Config: "conf", Key: "nginx.conf", Path: "/etc/nginx/nginx.conf"},
	)
	if d := diffFields(live, desired); len(d) != 0 {
		t.Fatalf("phantom drift: %v", fieldNames(d))
	}
	// A manifest silent about mounts keeps the ones attached in the console.
	if d := diffFields(live, appRes()); len(d) != 0 {
		t.Fatalf("silent manifest drifted: %v", fieldNames(d))
	}
}
