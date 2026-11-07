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

package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// Namespaced per source marketplace so a different catalog never serves another's templates or reuses its ETag.
const keyPrefix = "miabi:marketplace:"

// Cache persists the synced marketplace bundle, per source marketplace.
type Cache interface {
	Load(ctx context.Context, source string) (data []byte, etag string, err error)
	// Save stores the bundle and ETag for source, with a TTL safety net.
	Save(ctx context.Context, source string, data []byte, etag string, ttl time.Duration) error
}

// Hashed so an operator-supplied URL can never shape the key.
func sourceKeys(source string) (bundle, etag string) {
	sum := sha256.Sum256([]byte(source))
	base := keyPrefix + hex.EncodeToString(sum[:8])
	return base + ":bundle", base + ":bundle:etag"
}

// RedisCache stores the bundle in Redis (already provisioned for cache + asynq).
type RedisCache struct{ rdb *redis.Client }

// NewRedisCache wraps a Redis client as a bundle cache.
func NewRedisCache(rdb *redis.Client) *RedisCache { return &RedisCache{rdb: rdb} }

func (c *RedisCache) Load(ctx context.Context, source string) ([]byte, string, error) {
	bundleKey, etagKey := sourceKeys(source)
	data, err := c.rdb.Get(ctx, bundleKey).Bytes()
	if err == redis.Nil {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	etag, err := c.rdb.Get(ctx, etagKey).Result()
	if err == redis.Nil {
		etag = ""
	} else if err != nil {
		return nil, "", err
	}
	return data, etag, nil
}

func (c *RedisCache) Save(ctx context.Context, source string, data []byte, etag string, ttl time.Duration) error {
	bundleKey, etagKey := sourceKeys(source)
	if err := c.rdb.Set(ctx, bundleKey, data, ttl).Err(); err != nil {
		return err
	}
	return c.rdb.Set(ctx, etagKey, etag, ttl).Err()
}
