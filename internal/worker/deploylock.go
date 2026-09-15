// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/leader"
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

// redisDeployLock is a per-app leader.Lease with a background refresh: the TTL is short so a crashed
// worker's lock frees quickly, but it is renewed while held so a long build never loses it.
type redisDeployLock struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisDeployLock builds a per-app deploy lock over the given Redis client.
func NewRedisDeployLock(rdb *redis.Client) DeployLock {
	return &redisDeployLock{rdb: rdb, ttl: 60 * time.Second}
}

func deployLockKey(appID uint) string { return fmt.Sprintf("miabi:deploylock:app:%d", appID) }

func (l *redisDeployLock) Acquire(ctx context.Context, appID uint) (bool, func(), error) {
	lease := leader.NewLease(l.rdb, deployLockKey(appID), l.ttl)
	ok, err := lease.Acquire(ctx)
	if err != nil || !ok {
		return false, nil, err
	}
	refreshCtx, stop := context.WithCancel(context.Background())
	go func() {
		t := time.NewTicker(l.ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-t.C:
				held, err := lease.Refresh(refreshCtx)
				switch {
				case err != nil:
					if refreshCtx.Err() == nil {
						logger.Warn("deploy lock: refresh failed", "app", appID, "error", err)
					}
				case !held:
					// It expired, and another deploy may hold it now; there is nothing left to renew.
					logger.Warn("deploy lock: lost while the deploy was running", "app", appID)
					return
				}
			}
		}
	}()
	release := func() {
		stop()
		if err := lease.Release(context.Background()); err != nil {
			logger.Warn("deploy lock: release failed", "app", appID, "error", err)
		}
	}
	return true, release, nil
}
