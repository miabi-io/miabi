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

package worker

import "sync"

// clusterSlots caps deploys per cluster so one slow cluster cannot hold every worker slot.
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
