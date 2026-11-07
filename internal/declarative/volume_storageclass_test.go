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

package declarative_test

import (
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

func volumeSet(spec *d.VolumeSpec) *d.ResourceSet {
	set := d.NewResourceSet()
	set.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindVolume,
		Metadata: d.Meta{Name: "data"}, Volume: spec,
	})
	return set
}

func volumeChange(t *testing.T, desired, actual *d.VolumeSpec) d.Change {
	t.Helper()
	plan := d.BuildPlan(volumeSet(desired), volumeSet(actual), d.PlanOptions{IncludeNoop: true})
	for _, ch := range plan.Changes {
		if ch.Kind == d.KindVolume && ch.Name == "data" {
			return ch
		}
	}
	t.Fatal("no change planned for the volume")
	return d.Change{}
}

// An unstated class must not drift, so a repository stays portable across installs.
func TestUnstatedStorageClassDoesNotDrift(t *testing.T) {
	ch := volumeChange(t,
		&d.VolumeSpec{},
		&d.VolumeSpec{StorageClass: "ssd-fast"},
	)
	if ch.Action != d.ActionNoop {
		t.Fatalf("action = %s, want noop (fields: %+v)", ch.Action, ch.Fields)
	}
}

// A matching class must not report a phantom update on every apply.
func TestMatchingStorageClassIsNoop(t *testing.T) {
	ch := volumeChange(t,
		&d.VolumeSpec{StorageClass: "ssd-fast"},
		&d.VolumeSpec{StorageClass: "ssd-fast"},
	)
	if ch.Action != d.ActionNoop {
		t.Fatalf("action = %s, want noop (fields: %+v)", ch.Action, ch.Fields)
	}
}

// A changed class must appear in the diff, or the immutability refusal could never fire.
func TestChangedStorageClassIsVisibleToTheDiff(t *testing.T) {
	ch := volumeChange(t,
		&d.VolumeSpec{StorageClass: "bulk"},
		&d.VolumeSpec{StorageClass: "ssd-fast"},
	)
	if ch.Action != d.ActionUpdate {
		t.Fatalf("action = %s, want update", ch.Action)
	}
	var found bool
	for _, f := range ch.Fields {
		if f.Field == "storage.class" {
			found = true
			if f.From != "ssd-fast" || f.To != "bulk" {
				t.Fatalf("storage.class diff = %q -> %q, want ssd-fast -> bulk", f.From, f.To)
			}
		}
	}
	if !found {
		t.Fatalf("storage.class missing from the diff: %+v", ch.Fields)
	}
}

func TestVolumeSizeComparesAsBytes(t *testing.T) {
	ch := volumeChange(t,
		&d.VolumeSpec{Size: "5Gi"},
		&d.VolumeSpec{Size: "5368709120"},
	)
	if ch.Action != d.ActionNoop {
		t.Fatalf("action = %s, want noop (fields: %+v)", ch.Action, ch.Fields)
	}

	grown := volumeChange(t, &d.VolumeSpec{Size: "10Gi"}, &d.VolumeSpec{Size: "5Gi"})
	if grown.Action != d.ActionUpdate {
		t.Fatalf("action = %s, want update", grown.Action)
	}
}

// An unstated size leaves a console expansion alone.
func TestUnstatedVolumeSizeDoesNotDrift(t *testing.T) {
	ch := volumeChange(t, &d.VolumeSpec{}, &d.VolumeSpec{Size: "20Gi"})
	if ch.Action != d.ActionNoop {
		t.Fatalf("action = %s, want noop (fields: %+v)", ch.Action, ch.Fields)
	}
}

func TestVolumeSizeBytes(t *testing.T) {
	cases := map[string]int64{"": 0, "0": 0, "5Gi": 5 << 30, "512Mi": 512 << 20}
	for in, want := range cases {
		got, err := (&d.VolumeSpec{Size: in}).SizeBytes()
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Fatalf("%q = %d, want %d", in, got, want)
		}
	}
}
