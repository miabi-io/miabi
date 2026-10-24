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
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// EventRestrictionChanged is the only event v1 sends.
const EventRestrictionChanged = "billing.restriction.changed"

// RestrictionChanged is the data of billing.restriction.changed.
type RestrictionChanged struct {
	ExternalID string `json:"external_id"`
	From       string `json:"from"`
	To         string `json:"to"`
}

// Event is what the platform receives at its notify URL (contract §4).
type Event struct {
	Sequence   int64           `json:"sequence"`
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurred_at"`
	Timestamp  string          `json:"timestamp"`
	Data       json.RawMessage `json:"data"`
}

// eventBatch bounds one delivery run; the next run continues.
const eventBatch = 50

// maxEventBackoff caps the wait between attempts at one event.
const maxEventBackoff = time.Hour

// deliveredRetention is how long delivered events are kept for support.
const deliveredRetention = 30 * 24 * time.Hour

func enqueueEvent(tx *gorm.DB, typ string, data any, now time.Time) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return tx.Create(&models.BillingEvent{Type: typ, Data: string(raw), OccurredAt: now, NextAttemptAt: now}).Error
}

// Sign renders the X-Miabi-Signature of body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func eventBackoff(attempts int) time.Duration {
	if attempts > 12 {
		return maxEventBackoff
	}
	if d := time.Duration(1<<attempts) * time.Minute; d < maxEventBackoff {
		return d
	}
	return maxEventBackoff
}

// DeliverEvents sends due events in sequence order and stops at the first failure, so the console
// never sees a later event before an earlier one it could still receive. Nothing is sent while
// this Miabi is not connected; events wait.
func (s *Service) DeliverEvents(ctx context.Context) error {
	conn, err := s.Connection()
	if err != nil || conn == nil || !conn.Connected || conn.NotifyURL == "" {
		return nil
	}
	secret, err := s.NotifySecret()
	if err != nil || secret == "" {
		return nil
	}
	now := s.now()
	var due []models.BillingEvent
	if err := s.db.Where("delivered_at IS NULL").Order("id").Limit(eventBatch).Find(&due).Error; err != nil {
		return err
	}
	for i := range due {
		ev := &due[i]
		if ev.NextAttemptAt.After(now) {
			return nil
		}
		sendErr := s.sendEvent(ctx, conn, secret, ev)
		if sendErr == nil {
			delivered := s.now()
			if err := s.db.Model(ev).Updates(map[string]any{"delivered_at": delivered, "attempts": ev.Attempts + 1, "last_error": ""}).Error; err != nil {
				return err
			}
			continue
		}
		next := now.Add(eventBackoff(ev.Attempts))
		if err := s.db.Model(ev).Updates(map[string]any{"attempts": ev.Attempts + 1, "next_attempt_at": next, "last_error": sendErr.Error()}).Error; err != nil {
			return err
		}
		return fmt.Errorf("billing event %d: %w", ev.ID, sendErr)
	}
	return nil
}

func (s *Service) sendEvent(ctx context.Context, conn *Connection, secret string, ev *models.BillingEvent) error {
	body, err := json.Marshal(Event{Sequence: ev.ID, Type: ev.Type, OccurredAt: ev.OccurredAt.UTC().Format(time.RFC3339),
		Timestamp: s.now().UTC().Format(time.RFC3339), Data: json.RawMessage(ev.Data)})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conn.NotifyURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Miabi-Signature", Sign(secret, body))
	req.Header.Set("X-Miabi-Instance", conn.ConsoleInstanceID)
	req.Header.Set("X-Miabi-Event", ev.Type)
	req.Header.Set("X-Miabi-Delivery", strconv.FormatInt(ev.ID, 10))
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errors.New(resp.Status + ": " + string(bytes.TrimSpace(msg)))
	}
	return nil
}

// PurgeEvents deletes delivered events past their retention.
func (s *Service) PurgeEvents() error {
	return s.db.Where("delivered_at IS NOT NULL AND delivered_at < ?", s.now().Add(-deliveredRetention)).Delete(&models.BillingEvent{}).Error
}
