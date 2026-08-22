// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache key prefix. The bundle JSON and its ETag are stored separately so a
// conditional sync can read the ETag without decoding the whole bundle. Shared
// by the server and the worker, surviving restarts.
//
// Both are namespaced by the marketplace they came from, so bundles from
// different catalogs never collide — changing MIABI_MARKETPLACE_URL, or being
// held to the official one by an expired license, must not serve the other
// catalog's templates or hand its ETag to the new source (which would answer 304
// and pin the stale bundle).
const keyPrefix = "miabi:marketplace:"

// Cache persists the synced marketplace bundle, per source marketplace.
type Cache interface {
	// Load returns the cached bundle and its ETag for source. A cache miss returns
	// nil data and an empty ETag with a nil error.
	Load(ctx context.Context, source string) (data []byte, etag string, err error)
	// Save stores the bundle and ETag for source, with a TTL safety net.
	Save(ctx context.Context, source string, data []byte, etag string, ttl time.Duration) error
}

// sourceKeys returns the bundle and ETag keys for a marketplace URL. The URL is
// hashed rather than embedded so an arbitrary operator-supplied value can never
// shape the key.
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
