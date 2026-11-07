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

package analytics

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// LiveTracker counts distinct active visitors per workspace and app from the Goma event stream.
type LiveTracker struct {
	rdb    *redis.Client
	window time.Duration

	// Keys this process has written, so it knows what to sweep.
	mu     sync.Mutex
	scopes map[string]struct{}
}

// DefaultLiveWindow follows the common analytics convention (Plausible, Cloudflare, Fathom).
const DefaultLiveWindow = 5 * time.Minute

// Distinct from Goma's own visitor set in the same Redis.
const liveKeyPrefix = "miabi:live:"

// Drops a scope that stops receiving traffic even if no worker is left to sweep it.
const liveKeyTTL = time.Hour

// LiveVisit is one visitor sighting: the scope it belongs to, and who.
type LiveVisit struct {
	Workspace uint
	App       uint
	VID       string
}

func NewLiveTracker(rdb *redis.Client, window time.Duration) *LiveTracker {
	if window <= 0 {
		window = DefaultLiveWindow
	}
	return &LiveTracker{rdb: rdb, window: window, scopes: map[string]struct{}{}}
}

// Window is the "active within" period the counts describe.
func (t *LiveTracker) Window() time.Duration { return t.window }

// scopeKey builds the key for one scope. app 0 is the workspace-wide scope.
func scopeKey(ws, app uint) string {
	if app == 0 {
		return fmt.Sprintf("%sws%d", liveKeyPrefix, ws)
	}
	return fmt.Sprintf("%sws%d:app%d", liveKeyPrefix, ws, app)
}

// Touch is best-effort: a Redis failure costs a live count, never an ingest.
func (t *LiveTracker) Touch(ctx context.Context, visits []LiveVisit) {
	if t == nil || t.rdb == nil || len(visits) == 0 {
		return
	}
	now := float64(time.Now().Unix())

	byKey := make(map[string][]redis.Z, 4)
	for _, v := range visits {
		if v.Workspace == 0 || v.VID == "" {
			continue
		}
		z := redis.Z{Score: now, Member: v.VID}
		byKey[scopeKey(v.Workspace, 0)] = append(byKey[scopeKey(v.Workspace, 0)], z)
		if v.App != 0 {
			byKey[scopeKey(v.Workspace, v.App)] = append(byKey[scopeKey(v.Workspace, v.App)], z)
		}
	}
	if len(byKey) == 0 {
		return
	}

	pipe := t.rdb.Pipeline()
	for key, members := range byKey {
		pipe.ZAdd(ctx, key, members...)
		pipe.Expire(ctx, key, liveKeyTTL)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return // best-effort; don't remember scopes we failed to write
	}

	t.mu.Lock()
	for key := range byKey {
		t.scopes[key] = struct{}{}
	}
	t.mu.Unlock()
}

// Count is exact regardless of sweeps because the window is applied as a score range; app 0 means the workspace.
func (t *LiveTracker) Count(ctx context.Context, ws, app uint) (int64, error) {
	if t == nil || t.rdb == nil || ws == 0 {
		return 0, nil
	}
	since := time.Now().Add(-t.window).Unix()
	n, err := t.rdb.ZCount(ctx, scopeKey(ws, app), strconv.FormatInt(since, 10), "+inf").Result()
	if err == redis.Nil {
		return 0, nil
	}
	return n, err
}

// Sweep is housekeeping only: counts are already window-correct, so failures are never fatal.
func (t *LiveTracker) Sweep(ctx context.Context) {
	if t == nil || t.rdb == nil {
		return
	}
	t.mu.Lock()
	keys := make([]string, 0, len(t.scopes))
	for k := range t.scopes {
		keys = append(keys, k)
	}
	t.mu.Unlock()
	if len(keys) == 0 {
		return
	}

	cutoff := "(" + strconv.FormatInt(time.Now().Add(-t.window).Unix(), 10)
	pipe := t.rdb.Pipeline()
	for _, k := range keys {
		pipe.ZRemRangeByScore(ctx, k, "-inf", cutoff)
	}
	_, _ = pipe.Exec(ctx)
}
