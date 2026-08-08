// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// LiveTracker answers "how many distinct visitors are active right now" from the same Goma event
// stream the rollups use, but per workspace and per app rather than one gateway-wide gauge. One sorted
// set per scope keys on the daily-salted visitor id, so counting and expiry are both O(log N).
type LiveTracker struct {
	rdb    *redis.Client
	window time.Duration

	// scopes are the keys this process has written, so it knows what to sweep.
	// Bounded by the number of workspaces and apps with traffic.
	mu     sync.Mutex
	scopes map[string]struct{}
}

// DefaultLiveWindow is how far back a visitor still counts as "live" — the
// convention analytics products settled on (Plausible, Cloudflare, Fathom).
const DefaultLiveWindow = 5 * time.Minute

// liveKeyPrefix namespaces these keys. Miabi-owned, distinct from Goma's own
// visitor set in the same Redis.
const liveKeyPrefix = "miabi:live:"

// liveKeyTTL drops a scope that stops receiving traffic even if no worker is
// left to sweep it. Refreshed on every write.
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

// Touch records a batch of sightings as active now. Callers batch by their natural boundary — one
// stream read — so this is a handful of round trips per batch rather than one per event. Best-effort:
// a Redis failure costs a live count, never an ingest.
func (t *LiveTracker) Touch(ctx context.Context, visits []LiveVisit) {
	if t == nil || t.rdb == nil || len(visits) == 0 {
		return
	}
	now := float64(time.Now().Unix())

	// Group by scope so each key takes one multi-member ZADD. A visitor seen
	// twice in a batch is one member either way — the score just lands on the
	// later value.
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

// Count returns the distinct visitors active within the window for a scope
// (app 0 = the whole workspace). Exact, and unaffected by how recently a sweep
// ran, because the window is applied as a score range.
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

// Sweep drops visitors who have fallen outside the window from every scope this
// process writes to. Purely housekeeping — counts are already window-correct —
// so a failure is logged by the caller at most, never fatal.
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
