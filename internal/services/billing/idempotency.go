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

package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm/clause"
)

// IdempotencyTTL is how long a stored answer is replayed.
const IdempotencyTTL = 24 * time.Hour

// abandonAfter frees a key whose request never finished (the process died mid-request).
const abandonAfter = 5 * time.Minute

// Idempotency errors, answered as the contract says.
var (
	ErrKeyReused = errors.New("this Idempotency-Key was used with a different request")
	ErrInFlight  = errors.New("a request with this Idempotency-Key is still running; retry later")
)

// Stored is a finished answer to replay.
type Stored struct {
	Status int
	Body   []byte
}

// RequestHash fingerprints a request so a key reused for a different one is caught.
func RequestHash(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method + " " + path + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// BeginIdempotent claims key for the request. It returns the stored answer when the same request
// already finished, ErrKeyReused for a different request, ErrInFlight while it is still running,
// or (nil, nil) when the caller should run the request and then call FinishIdempotent.
func (s *Service) BeginIdempotent(key, hash string) (*Stored, error) {
	now := s.now()
	for attempt := 0; attempt < 2; attempt++ {
		row := models.BillingIdempotencyKey{Key: key, RequestHash: hash, ExpiresAt: now.Add(IdempotencyTTL), CreatedAt: now}
		res := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 1 {
			return nil, nil
		}
		var cur models.BillingIdempotencyKey
		if err := s.db.First(&cur, "key = ?", key).Error; err != nil {
			continue // removed meanwhile: try to claim it again
		}
		expired := !cur.ExpiresAt.After(now)
		abandoned := !cur.Done && now.Sub(cur.CreatedAt) > abandonAfter
		if expired || abandoned {
			s.db.Where("key = ? AND created_at = ?", key, cur.CreatedAt).Delete(&models.BillingIdempotencyKey{})
			continue
		}
		if cur.RequestHash != hash {
			return nil, ErrKeyReused
		}
		if !cur.Done {
			return nil, ErrInFlight
		}
		return &Stored{Status: cur.StatusCode, Body: []byte(cur.Response)}, nil
	}
	return nil, ErrInFlight
}

// FinishIdempotent stores the answer for replay, except server errors and 402s, which are not
// final: their key is released so a retry runs the request again.
func (s *Service) FinishIdempotent(key string, status int, body []byte) error {
	if status >= 500 || status == 402 {
		return s.db.Where("key = ? AND done = ?", key, false).Delete(&models.BillingIdempotencyKey{}).Error
	}
	return s.db.Model(&models.BillingIdempotencyKey{}).Where("key = ?", key).
		Updates(map[string]any{"done": true, "status_code": status, "response": string(body)}).Error
}

// PurgeIdempotency deletes expired keys.
func (s *Service) PurgeIdempotency() error {
	return s.db.Where("expires_at <= ?", s.now()).Delete(&models.BillingIdempotencyKey{}).Error
}
