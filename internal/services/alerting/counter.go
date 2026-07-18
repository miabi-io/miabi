// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package alerting

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Counter holds the alert engine's hot state — windowed signal counts (crash-loop
// detection) and per-key notify cooldowns (rate-limiting). Backed by Redis so it
// is correct across control-plane replicas and survives a worker restart; an
// in-memory implementation backs the tests.
type Counter interface {
	// Incr adds 1 to the window counter for key and returns the new value. The
	// window TTL is set on first increment (fixed window).
	Incr(ctx context.Context, key string, window time.Duration) (int64, error)
	// Reset clears a key (e.g. when its condition resolves).
	Reset(ctx context.Context, key string) error
	// AllowNotify reports whether a notification for key may be sent now, arming a
	// cooldown so a re-firing alert can't notify more than once per window.
	AllowNotify(ctx context.Context, key string, cooldown time.Duration) (bool, error)
}

// Redis

type redisCounter struct{ rdb *redis.Client }

// NewRedisCounter backs the engine with Redis.
func NewRedisCounter(rdb *redis.Client) Counter { return &redisCounter{rdb: rdb} }

func (c *redisCounter) Incr(ctx context.Context, key string, window time.Duration) (int64, error) {
	k := "alertcnt:" + key
	n, err := c.rdb.Incr(ctx, k).Result()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		_ = c.rdb.Expire(ctx, k, window).Err()
	}
	return n, nil
}

func (c *redisCounter) Reset(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, "alertcnt:"+key).Err()
}

func (c *redisCounter) AllowNotify(ctx context.Context, key string, cooldown time.Duration) (bool, error) {
	ok, err := c.rdb.SetNX(ctx, "alertcd:"+key, 1, cooldown).Result()
	return ok, err
}

type memCounter struct {
	mu      sync.Mutex
	counts  map[string]int64
	cdUntil map[string]time.Time
	now     func() time.Time
}

// NewMemoryCounter is a process-local counter for tests and single-process runs
// without Redis. now defaults to time.Now when nil.
func NewMemoryCounter(now func() time.Time) Counter {
	if now == nil {
		now = time.Now
	}
	return &memCounter{counts: map[string]int64{}, cdUntil: map[string]time.Time{}, now: now}
}

func (c *memCounter) Incr(_ context.Context, key string, _ time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key]++
	return c.counts[key], nil
}

func (c *memCounter) Reset(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.counts, key)
	return nil
}

func (c *memCounter) AllowNotify(_ context.Context, key string, cooldown time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if until, ok := c.cdUntil[key]; ok && now.Before(until) {
		return false, nil
	}
	c.cdUntil[key] = now.Add(cooldown)
	return true, nil
}
