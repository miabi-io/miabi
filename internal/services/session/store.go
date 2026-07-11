// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package session maintains a Redis-backed revocation list for JWT sessions,
// keyed by the token's jti claim.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const revokedPrefix = "session:revoked:"
const resetSessionPrefix = "auth:pwreset:"

// ResetSessionTTL bounds how long a forced-password-change reset token is valid.
const ResetSessionTTL = 15 * time.Minute

// ErrResetSession is returned when a reset-session token is missing, expired, or
// already used.
var ErrResetSession = errors.New("reset session is invalid or has expired")

type Store struct {
	redis *redis.Client
}

func NewStore(client *redis.Client) *Store { return &Store{redis: client} }

// MarkRevoked blacklists a jti until its natural expiry.
func (s *Store) MarkRevoked(ctx context.Context, jti string, expiresAt time.Time) {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return
	}
	s.redis.Set(ctx, revokedPrefix+jti, "1", ttl)
}

// IsRevoked reports whether a jti is blacklisted. Fails open on Redis errors.
func (s *Store) IsRevoked(ctx context.Context, jti string) bool {
	n, err := s.redis.Exists(ctx, revokedPrefix+jti).Result()
	if err != nil {
		return false
	}
	return n > 0
}

// CreateResetSession stores a short-lived, single-use token authorizing exactly
// one action — setting a new password for userID — and returns it. Used by the
// forced-password-change flow so a user with an admin-set/reset password never
// receives a full session until they've chosen their own password.
func (s *Store) CreateResetSession(ctx context.Context, userID uint) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if err := s.redis.Set(ctx, resetSessionPrefix+token, strconv.FormatUint(uint64(userID), 10), ResetSessionTTL).Err(); err != nil {
		return "", err
	}
	return token, nil
}

// ConsumeResetSession atomically validates and deletes a reset token, returning
// the user it belongs to. Single-use: a second call with the same token fails.
func (s *Store) ConsumeResetSession(ctx context.Context, token string) (uint, error) {
	if token == "" {
		return 0, ErrResetSession
	}
	val, err := s.redis.GetDel(ctx, resetSessionPrefix+token).Result()
	if err != nil {
		return 0, ErrResetSession
	}
	id, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return 0, ErrResetSession
	}
	return uint(id), nil
}
