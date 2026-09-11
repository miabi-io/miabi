// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import "testing"

func TestClusterSlotsCapEachClusterSeparately(t *testing.T) {
	slots := newClusterSlots(2)

	release, ok1 := slots.acquire(1)
	_, ok2 := slots.acquire(1)
	if !ok1 || !ok2 {
		t.Fatal("the first two deploys of a cluster were refused")
	}
	if _, ok := slots.acquire(1); ok {
		t.Fatal("a third concurrent deploy was let into a cluster capped at two")
	}
	if _, ok := slots.acquire(2); !ok {
		t.Fatal("a busy cluster blocked a deploy to another cluster")
	}

	release()
	release()
	if _, ok := slots.acquire(1); !ok {
		t.Fatal("a released slot could not be reused")
	}
	if _, ok := slots.acquire(1); ok {
		t.Fatal("releasing the same slot twice freed two")
	}
}
