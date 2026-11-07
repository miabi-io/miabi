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

// Package session maintains a Redis-backed JWT revocation list keyed by jti.
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

// CreateResetSession lets a user with an admin-set password change it without ever getting a full session.
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
