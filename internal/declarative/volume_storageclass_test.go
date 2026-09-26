// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// A manifest that names no class must not drift against a volume that landed on one: the same
// repository has to stay portable across installs whose disks differ.
func TestUnstatedStorageClassDoesNotDrift(t *testing.T) {
	ch := volumeChange(t,
		&d.VolumeSpec{},
		&d.VolumeSpec{StorageClass: "ssd-fast"},
	)
	if ch.Action != d.ActionNoop {
		t.Fatalf("action = %s, want noop (fields: %+v)", ch.Action, ch.Fields)
	}
}

// A class the manifest states and the volume already has is in sync — the diff must not report a
// phantom update every time the manifest is applied.
func TestMatchingStorageClassIsNoop(t *testing.T) {
	ch := volumeChange(t,
		&d.VolumeSpec{StorageClass: "ssd-fast"},
		&d.VolumeSpec{StorageClass: "ssd-fast"},
	)
	if ch.Action != d.ActionNoop {
		t.Fatalf("action = %s, want noop (fields: %+v)", ch.Action, ch.Fields)
	}
}

// Changing a stated class must surface as a field diff so the apply can refuse it. If it did not
// appear here, the immutability check could never fire and a git push would move data silently.
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

// Size is compared as canonical bytes, so "5Gi" in a manifest matches the byte count the snapshot
// reports rather than reading as a change on every apply.
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

// A manifest silent about size leaves the volume's capacity alone, so expanding it in the console does
// not turn every later apply into a change.
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
