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

package middlewares

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/redis/go-redis/v9"
)

// When Redis is down it fails open, or with localFallback uses a per-process limiter.
func RateLimit(rdb *redis.Client, limit int, window time.Duration, localFallback bool) okapi.Middleware {
	var (
		fallback *memLimiter
		lastWarn atomic.Int64 // unix nanos of the last "redis down" log, to throttle it
	)
	if localFallback {
		fallback = newMemLimiter()
	}
	warnOnce := func(err error) {
		now := time.Now().UnixNano()
		if prev := lastWarn.Load(); now-prev > int64(30*time.Second) && lastWarn.CompareAndSwap(prev, now) {
			logger.Warn("rate limiter: Redis unavailable, using in-memory fallback", "error", err)
		}
	}

	return func(c *okapi.Context) error {
		if limit <= 0 {
			return c.Next()
		}
		if rdb == nil {
			if fallback != nil {
				return applyMemLimit(c, fallback, limit, window)
			}
			return c.Next()
		}
		ctx := c.Request().Context()
		key := "rl:" + c.Request().URL.Path + ":" + c.RealIP()

		n, err := rdb.Incr(ctx, key).Result()
		if err != nil {
			if fallback != nil {
				warnOnce(err)
				return applyMemLimit(c, fallback, limit, window)
			}
			return c.Next() // fail open
		}
		if n == 1 {
			_ = rdb.Expire(ctx, key, window).Err()
		}
		if n > int64(limit) {
			return c.AbortTooManyRequests("too many requests — please slow down and try again shortly")
		}
		return c.Next()
	}
}

func applyMemLimit(c *okapi.Context, m *memLimiter, limit int, window time.Duration) error {
	if !m.allow(c.Request().URL.Path+":"+c.RealIP(), limit, window) {
		return c.AbortTooManyRequests("too many requests — please slow down and try again shortly")
	}
	return c.Next()
}

// The map is swept once it grows past a bound so it can't leak memory.
type memLimiter struct {
	mu   sync.Mutex
	hits map[string]memWindow
}

type memWindow struct {
	count   int
	resetAt time.Time
}

func newMemLimiter() *memLimiter { return &memLimiter{hits: make(map[string]memWindow)} }

// memLimiterSweepAt is the map size that triggers eviction of expired windows.
const memLimiterSweepAt = 10000

func (m *memLimiter) allow(key string, limit int, window time.Duration) bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.hits) > memLimiterSweepAt {
		for k, w := range m.hits {
			if now.After(w.resetAt) {
				delete(m.hits, k)
			}
		}
	}
	w, ok := m.hits[key]
	if !ok || now.After(w.resetAt) {
		m.hits[key] = memWindow{count: 1, resetAt: now.Add(window)}
		return true
	}
	w.count++
	m.hits[key] = w
	return w.count <= limit
}
