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

// Package siem streams the audit log at-least-once; the cursor advances only on a successful ship.
package siem

import (
	"context"
	"fmt"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Flag is the entitlement that unlocks SIEM streaming.
const Flag = "siem_stream"

const batchSize = 500

// Kept minimal so this package does not import the enterprise seam.
type EntitlementChecker interface{ Has(flag string) bool }

// An error leaves the cursor un-advanced so the batch is retried next tick.
type Sink interface {
	Ship(events []models.AuditLog) error
}

// Streamer ships audit events to all enabled SIEM targets.
type Streamer struct {
	audit  *repositories.AuditLogRepository
	config *repositories.SIEMConfigRepository
	ee     EntitlementChecker
}

func NewStreamer(audit *repositories.AuditLogRepository, config *repositories.SIEMConfigRepository, ee EntitlementChecker) *Streamer {
	return &Streamer{audit: audit, config: config, ee: ee}
}

// Enabled reports whether streaming is licensed.
func (s *Streamer) Enabled() bool { return s.ee != nil && s.ee.Has(Flag) }

// One target's failure never affects another.
func (s *Streamer) Tick(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	configs, err := s.config.FindEnabled()
	if err != nil {
		return err
	}
	for i := range configs {
		s.shipOne(&configs[i])
	}
	return nil
}

func (s *Streamer) shipOne(cfg *models.SIEMConfig) {
	sink, err := s.sinkFor(cfg)
	if err != nil {
		_ = s.config.RecordError(cfg.ID, err.Error())
		return
	}
	cursor := cfg.LastShippedID
	for {
		rows, err := s.audit.Since(cursor, batchSize)
		if err != nil {
			_ = s.config.RecordError(cfg.ID, err.Error())
			return
		}
		if len(rows) == 0 {
			return
		}
		if err := sink.Ship(rows); err != nil {
			logger.Warn("siem: ship failed", "target", cfg.Name, "error", err)
			_ = s.config.RecordError(cfg.ID, err.Error())
			return // keep the cursor; retry next tick (at-least-once)
		}
		cursor = rows[len(rows)-1].ID
		if err := s.config.AdvanceCursor(cfg.ID, cursor); err != nil {
			logger.Warn("siem: cursor advance failed", "target", cfg.Name, "error", err)
			return
		}
		if len(rows) < batchSize {
			return // caught up
		}
	}
}

// Test ships a single synthetic event to verify a target's connectivity.
func (s *Streamer) Test(cfg *models.SIEMConfig, event models.AuditLog) error {
	sink, err := s.sinkFor(cfg)
	if err != nil {
		return err
	}
	return sink.Ship([]models.AuditLog{event})
}

func (s *Streamer) sinkFor(cfg *models.SIEMConfig) (Sink, error) {
	switch cfg.Sink {
	case models.SIEMSinkSyslog:
		return newSyslogSink(cfg)
	case models.SIEMSinkWebhook:
		auth := ""
		if cfg.AuthHeaderEnc != "" {
			if v, err := crypto.Decrypt(cfg.AuthHeaderEnc); err == nil {
				auth = v
			}
		}
		return newWebhookSink(cfg.Endpoint, auth), nil
	default:
		return nil, fmt.Errorf("unsupported siem sink: %s", cfg.Sink)
	}
}
