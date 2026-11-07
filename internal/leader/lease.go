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

package leader

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/redis/go-redis/v9"
)

// Every operation compares the token, so a lease taken by someone else is never renewed or deleted.
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

// Renews when already held, so a lost acquire reply doesn't lock its own holder out.
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
