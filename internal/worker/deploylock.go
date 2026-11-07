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
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/leader"
	"github.com/redis/go-redis/v9"
)

// DeployLock serializes deploys per app so concurrent ones can't race on versions or the container swap; nil disables.
type DeployLock interface {
	// ok=false means another deploy holds the lock and the caller should defer and retry.
	Acquire(ctx context.Context, appID uint) (ok bool, release func(), err error)
}

// Short TTL so a crashed worker's lock frees quickly, renewed while held so a long build never loses it.
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
