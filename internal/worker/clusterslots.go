// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import "sync"

// clusterSlots caps concurrent deploys per cluster, so one slow or unreachable cluster cannot hold every
// worker slot while deploys to other clusters wait.
type clusterSlots struct {
	mu    sync.Mutex
	limit int
	used  map[uint]int
}

func newClusterSlots(limit int) *clusterSlots {
	if limit < 1 {
		limit = 1
	}
	return &clusterSlots{limit: limit, used: map[uint]int{}}
}

// acquire takes a slot for the cluster and returns its release, or reports false when the cluster is at its cap.
func (c *clusterSlots) acquire(clusterID uint) (func(), bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used[clusterID] >= c.limit {
		return nil, false
	}
	c.used[clusterID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.used[clusterID]--
			c.mu.Unlock()
		})
	}, true
}
