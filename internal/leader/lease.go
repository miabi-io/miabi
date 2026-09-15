// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package leader

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/redis/go-redis/v9"
)

// Lease is one holder's claim on a Redis key with a TTL. Every operation compares the holder's
// token, so a lease that expired and was taken by someone else is never renewed or deleted.
type Lease struct {
	rdb   redis.Cmdable
	key   string
	token string
	ttl   time.Duration
}

// NewLease returns an unheld lease on key, with a token unique to this lease.
func NewLease(rdb redis.Cmdable, key string, ttl time.Duration) *Lease {
	return &Lease{rdb: rdb, key: key, token: rand.Text(), ttl: ttl}
}

var acquireScript = redis.NewScript(`
local v = redis.call("get", KEYS[1])
if not v then
  redis.call("set", KEYS[1], ARGV[1], "PX", ARGV[2])
  return 1
end
if v == ARGV[1] then
  return redis.call("pexpire", KEYS[1], ARGV[2])
end
return 0`)

var refreshScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("pexpire", KEYS[1], ARGV[2])
end
return 0`)

var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
end
return 0`)

// Acquire takes the key when it is free and renews it when this lease already holds it, so an
// acquire whose reply was lost doesn't lock its own holder out until the key expires.
func (l *Lease) Acquire(ctx context.Context) (bool, error) {
	n, err := acquireScript.Run(ctx, l.rdb, []string{l.key}, l.token, l.ttl.Milliseconds()).Int()
	return n == 1, err
}

// Refresh renews the key only while this lease holds it; false means the lease was lost.
func (l *Lease) Refresh(ctx context.Context) (bool, error) {
	n, err := refreshScript.Run(ctx, l.rdb, []string{l.key}, l.token, l.ttl.Milliseconds()).Int()
	return n == 1, err
}

// Release deletes the key if this lease still holds it.
func (l *Lease) Release(ctx context.Context) error {
	return releaseScript.Run(ctx, l.rdb, []string{l.key}, l.token).Err()
}
