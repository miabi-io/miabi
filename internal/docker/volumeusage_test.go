// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"testing"

	"github.com/docker/docker/api/types/volume"
)

func TestVolumeUsageFrom(t *testing.T) {
	vols := []*volume.Volume{
		{Name: "a", UsageData: &volume.UsageData{Size: 100, RefCount: 2}},
		{Name: "nosize", UsageData: nil},                              // never sized
		{Name: "notcomputed", UsageData: &volume.UsageData{Size: -1}}, // df didn't compute it
		nil, // defensive
		{Name: "b", UsageData: &volume.UsageData{Size: 0, RefCount: 0}}, // sized 0, reclaimable
	}
	got := volumeUsageFrom(vols)
	if len(got) != 2 {
		t.Fatalf("kept %d entries, want 2 (a, b): %+v", len(got), got)
	}
	if got[0].DockerName != "a" || got[0].Bytes != 100 || got[0].RefCount != 2 {
		t.Fatalf("entry a wrong: %+v", got[0])
	}
	if got[1].DockerName != "b" || got[1].Bytes != 0 || got[1].RefCount != 0 {
		t.Fatalf("entry b wrong: %+v", got[1])
	}
}
