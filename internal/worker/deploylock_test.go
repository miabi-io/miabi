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

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestLock(t *testing.T, ttl time.Duration) (*redisDeployLock, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &redisDeployLock{rdb: rdb, ttl: ttl}, mr
}

func TestDeployLockSerializesAnApp(t *testing.T) {
	l, _ := newTestLock(t, time.Minute)
	ctx := context.Background()

	ok, release, err := l.Acquire(ctx, 7)
	if err != nil || !ok {
		t.Fatalf("first acquire = %v, %v; want held", ok, err)
	}
	if again, _, err := l.Acquire(ctx, 7); err != nil || again {
		t.Fatalf("second acquire of the same app = %v, %v; want refused", again, err)
	}
	other, releaseOther, err := l.Acquire(ctx, 8)
	if err != nil || !other {
		t.Fatalf("acquire of another app = %v, %v; want held", other, err)
	}
	releaseOther()

	release()
	ok, release, err = l.Acquire(ctx, 7)
	if err != nil || !ok {
		t.Fatalf("acquire after release = %v, %v; want held", ok, err)
	}
	release()
}

// A deploy whose lock expired must not free it for the deploy that took it over.
func TestDeployLockReleaseLeavesAnotherHoldersLock(t *testing.T) {
	l, mr := newTestLock(t, time.Minute)
	ctx := context.Background()
	ok, releaseFirst, err := l.Acquire(ctx, 9)
	if err != nil || !ok {
		t.Fatalf("acquire = %v, %v; want held", ok, err)
	}

	mr.FastForward(2 * time.Minute)
	ok, releaseSecond, err := l.Acquire(ctx, 9)
	if err != nil || !ok {
		t.Fatalf("acquire after expiry = %v, %v; want held", ok, err)
	}
	defer releaseSecond()

	releaseFirst()
	if !mr.Exists(deployLockKey(9)) {
		t.Fatal("the expired holder's release deleted the new holder's lock")
	}
}

// Regression: the refresher must not renew a lease that expired and was retaken by another holder.
func TestDeployLockRefreshNeverRenewsAnotherHoldersLock(t *testing.T) {
	ttl := 150 * time.Millisecond
	l, mr := newTestLock(t, ttl)
	ok, release, err := l.Acquire(context.Background(), 7)
	if err != nil || !ok {
		t.Fatalf("acquire = %v, %v; want held", ok, err)
	}
	defer release()

	key := deployLockKey(7)
	if err := mr.Set(key, "another-deploy"); err != nil {
		t.Fatal(err)
	}
	mr.SetTTL(key, time.Hour)

	time.Sleep(3 * ttl) // several refresh ticks
	if got := mr.TTL(key); got != time.Hour {
		t.Fatalf("TTL = %v; a deploy that no longer holds the lock renewed it", got)
	}
}

func TestDeployLockRefreshRenewsItsOwnLock(t *testing.T) {
	ttl := 150 * time.Millisecond
	l, mr := newTestLock(t, ttl)
	ok, release, err := l.Acquire(context.Background(), 7)
	if err != nil || !ok {
		t.Fatalf("acquire = %v, %v; want held", ok, err)
	}
	defer release()

	key := deployLockKey(7)
	mr.SetTTL(key, time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for mr.TTL(key) != ttl {
		if time.Now().After(deadline) {
			t.Fatalf("TTL = %v; the holder never renewed its lock", mr.TTL(key))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
