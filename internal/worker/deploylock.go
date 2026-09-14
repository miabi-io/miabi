// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/redis/go-redis/v9"
)

// DeployLock serializes deploys per application so two concurrent deploys of the same app can't race
// on release-version assignment or the active-container swap. Optional on the handler — a nil lock
// disables serialization (single-worker dev, tests).
type DeployLock interface {
	// Acquire tries to take the per-app deploy lock without blocking. ok=true means
	// the caller holds it and must call release when done; ok=false means another
	// deploy holds it and the caller should defer and retry later.
	Acquire(ctx context.Context, appID uint) (ok bool, release func(), err error)
}

// redisDeployLock is a Redis SET-NX lock with a background refresh: the TTL is short so a crashed
// worker's lock frees quickly, but it is renewed while held so a long build never loses it. Refresh
// and release both compare the token, so a lock that expired and was retaken is never touched.
type redisDeployLock struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisDeployLock builds a per-app deploy lock over the given Redis client.
func NewRedisDeployLock(rdb *redis.Client) DeployLock {
	return &redisDeployLock{rdb: rdb, ttl: 60 * time.Second}
}

func deployLockKey(appID uint) string { return fmt.Sprintf("miabi:deploylock:app:%d", appID) }

// releaseScript deletes the key only if it still holds our token.
var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
end
return 0`)

// refreshScript extends the key's TTL only if it still holds our token.
var refreshScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("pexpire", KEYS[1], ARGV[2])
end
return 0`)

func (l *redisDeployLock) Acquire(ctx context.Context, appID uint) (bool, func(), error) {
	key := deployLockKey(appID)
	// Unique per acquisition so release only ever deletes our own lock.
	token := fmt.Sprintf("%d-%d", appID, time.Now().UnixNano())
	ok, err := l.rdb.SetNX(ctx, key, token, l.ttl).Result()
	if err != nil {
		return false, nil, err
	}
	if !ok {
		return false, nil, nil
	}
	// Refresh the TTL at a third of its length while the deploy runs.
	refreshCtx, stop := context.WithCancel(context.Background())
	go func() {
		t := time.NewTicker(l.ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-t.C:
				held, err := refreshScript.Run(refreshCtx, l.rdb, []string{key}, token, l.ttl.Milliseconds()).Int()
				switch {
				case err != nil:
					if refreshCtx.Err() == nil {
						logger.Warn("deploy lock: refresh failed", "app", appID, "error", err)
					}
				case held == 0:
					// It expired, and another deploy may hold it now; there is nothing left to renew.
					logger.Warn("deploy lock: lost while the deploy was running", "app", appID)
					return
				}
			}
		}
	}()
	release := func() {
		stop()
		if err := releaseScript.Run(context.Background(), l.rdb, []string{key}, token).Err(); err != nil {
			logger.Warn("deploy lock: release failed", "app", appID, "error", err)
		}
	}
	return true, release, nil
}
