// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodestats

import "testing"

// describesNode is the rule the sampler applies: Docker's MemTotal is cgroup-aware and is the
// authority on what a node has; /proc's is not and reports the machine underneath.
func describesNode(sampledTotal uint64, dockerTotal int64) bool {
	if dockerTotal <= 0 {
		return true
	}
	delta := dockerTotal - int64(sampledTotal)
	if delta < 0 {
		delta = -delta
	}
	return delta*100/dockerTotal <= memTolerancePct
}

func TestSampleConsistency(t *testing.T) {
	const gb = int64(1) << 30

	cases := []struct {
		name    string
		sampled uint64
		docker  int64
		want    bool
	}{
		// An ordinary host: docker info MemTotal IS /proc's MemTotal, to the byte.
		{"bare metal node", uint64(12599369728), 12599369728, true},
		{"rounding drift is tolerated", uint64(12599369728 - 100), 12599369728, true},
		// The case that broke the dashboard: a 4 GB node on a 64 GB machine reports the machine.
		{"containerised node reports its host", uint64(64 * gb), 4 * gb, false},
		{"memory-limited vm", uint64(12 * gb), 512 * 1024 * 1024, false},
		// No figure from Docker: nothing to contradict the sample, so keep it.
		{"docker reports nothing", uint64(8 * gb), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describesNode(tc.sampled, tc.docker); got != tc.want {
				t.Fatalf("describesNode(%d, %d) = %v, want %v", tc.sampled, tc.docker, got, tc.want)
			}
		})
	}
}
